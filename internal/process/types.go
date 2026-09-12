package process

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ingot-agent/ingot/internal/image"
)

const MaxProcessJSONSize = int64(4 << 20)

type Record struct {
	ProcessVersion    int          `json:"process_version"`
	ProcessID         string       `json:"process_id"`
	Mode              string       `json:"mode"`
	Phase             string       `json:"phase"`
	SupervisorPID     int          `json:"supervisor_pid"`
	SupervisorBirthID string       `json:"supervisor_birth_id"`
	RuntimePID        *int         `json:"runtime_pid"`
	RuntimeBirthID    string       `json:"runtime_birth_id"`
	ImageID           string       `json:"image_id"`
	ArtifactDigest    string       `json:"artifact_digest"`
	Target            image.Target `json:"target"`
	RuntimeGeneration uint64       `json:"runtime_generation"`
	Argv              []string     `json:"argv"`
	StartedAt         time.Time    `json:"started_at"`
	LogPath           *string      `json:"log_path"`
}

type Control struct {
	ControlVersion int    `json:"control_version"`
	ProcessID      string `json:"process_id"`
	Endpoint       string `json:"endpoint"`
	Token          string `json:"token"`
}

type LastExit struct {
	ProcessVersion    int          `json:"process_version"`
	ProcessID         string       `json:"process_id"`
	Mode              string       `json:"mode"`
	SupervisorPID     int          `json:"supervisor_pid"`
	SupervisorBirthID string       `json:"supervisor_birth_id"`
	RuntimePID        *int         `json:"runtime_pid"`
	RuntimeBirthID    string       `json:"runtime_birth_id"`
	ImageID           string       `json:"image_id"`
	ArtifactDigest    string       `json:"artifact_digest"`
	Target            image.Target `json:"target"`
	RuntimeGeneration uint64       `json:"runtime_generation"`
	Argv              []string     `json:"argv"`
	StartedAt         time.Time    `json:"started_at"`
	ExitedAt          time.Time    `json:"exited_at"`
	ExitCode          int          `json:"exit_code"`
	Reason            string       `json:"reason"`
	Error             string       `json:"error"`
	LogPath           *string      `json:"log_path"`
}

type StartResult struct {
	StartVersion int       `json:"start_version"`
	ProcessID    string    `json:"process_id"`
	ReportedAt   time.Time `json:"reported_at"`
	Error        string    `json:"error"`
}

func RunDirectory(runtimeHome string) string { return filepath.Join(runtimeHome, "run") }
func ProcessPath(runtimeHome string) string {
	return filepath.Join(RunDirectory(runtimeHome), "process.json")
}
func ControlPath(runtimeHome string) string {
	return filepath.Join(RunDirectory(runtimeHome), "control.json")
}
func LastExitPath(runtimeHome string) string {
	return filepath.Join(RunDirectory(runtimeHome), "last-exit.json")
}
func SupervisorLockPath(runtimeHome string) string {
	return filepath.Join(RunDirectory(runtimeHome), "supervisor.lock")
}
func WriterLockPath(runtimeHome string) string {
	return filepath.Join(RunDirectory(runtimeHome), "writer.lock")
}
func StartResultPath(runtimeHome, processID string) string {
	return filepath.Join(RunDirectory(runtimeHome), "start-"+processID+".json")
}

func NewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	value := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", value[:8], value[8:12], value[12:16], value[16:20], value[20:]), nil
}

func ReadRecord(runtimeHome string) (*Record, error) {
	var value Record
	if err := readStrict(ProcessPath(runtimeHome), &value); err != nil {
		return nil, err
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return &value, nil
}
func ReadControl(runtimeHome string) (*Control, error) {
	var value Control
	if err := readStrict(ControlPath(runtimeHome), &value); err != nil {
		return nil, err
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return &value, nil
}
func ReadLastExit(runtimeHome string) (*LastExit, error) {
	var value LastExit
	if err := readStrict(LastExitPath(runtimeHome), &value); err != nil {
		return nil, err
	}
	if err := value.Validate(); err != nil {
		return nil, err
	}
	return &value, nil
}
func ReadStartResult(runtimeHome, processID string) (*StartResult, error) {
	if err := ValidateID(processID); err != nil {
		return nil, err
	}
	var value StartResult
	if err := readStrict(StartResultPath(runtimeHome, processID), &value); err != nil {
		return nil, err
	}
	if value.StartVersion != 1 || value.ProcessID != processID || value.ReportedAt.IsZero() || len(value.Error) > 512 {
		return nil, fmt.Errorf("INGOT-PROCESS-START-SCHEMA: invalid start result")
	}
	return &value, nil
}
func readStrict(path string, value any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxProcessJSONSize {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SIZE: %s exceeds limit", path)
	}
	data, err := io.ReadAll(io.LimitReader(file, MaxProcessJSONSize+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > MaxProcessJSONSize {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SIZE: %s exceeds limit", path)
	}
	return image.StrictDecode(data, value)
}
func writeJSON(path string, value any) error {
	switch value := value.(type) {
	case Record:
		if err := value.Validate(); err != nil {
			return err
		}
	case Control:
		if err := value.Validate(); err != nil {
			return err
		}
	case LastExit:
		if err := value.Validate(); err != nil {
			return err
		}
	case StartResult:
		if value.StartVersion != 1 || !validProcessID(value.ProcessID) || value.ReportedAt.IsZero() || len(value.Error) > 512 {
			return fmt.Errorf("INGOT-PROCESS-START-SCHEMA: invalid start result")
		}
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return image.AtomicWrite(path, append(data, '\n'), 0o600)
}

func WriteStartResult(runtimeHome, processID string, startErr error) error {
	if err := ValidateID(processID); err != nil {
		return err
	}
	result := StartResult{StartVersion: 1, ProcessID: processID, ReportedAt: time.Now().UTC(), Error: sanitizeError(startErr)}
	return writeJSON(StartResultPath(runtimeHome, processID), result)
}

func ValidateID(value string) error {
	if !validProcessID(value) {
		return fmt.Errorf("INGOT-PROCESS-ID: invalid process id")
	}
	return nil
}

func (record Record) Validate() error {
	if record.ProcessVersion != 1 || !validProcessID(record.ProcessID) || record.RuntimeGeneration == 0 || record.Argv == nil || record.StartedAt.IsZero() {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: invalid process record")
	}
	if record.Mode != "foreground" && record.Mode != "detached" {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: invalid mode")
	}
	if record.Phase != "starting" && record.Phase != "running" && record.Phase != "stopping" {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: invalid phase")
	}
	if record.SupervisorPID <= 0 || record.SupervisorBirthID == "" || !image.ValidDigest(record.ImageID) || !image.ValidDigest(record.ArtifactDigest) {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: invalid identity")
	}
	if err := record.Target.Validate(); err != nil {
		return err
	}
	if record.RuntimePID == nil {
		if record.Phase != "starting" || record.RuntimeBirthID != "" {
			return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: missing runtime identity")
		}
	} else if *record.RuntimePID <= 0 || record.RuntimeBirthID == "" {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: invalid runtime identity")
	}
	if err := validateArgv(record.Argv); err != nil {
		return err
	}
	if record.Mode == "foreground" && record.LogPath != nil {
		return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: foreground process has a log path")
	}
	if record.Mode == "detached" {
		if record.LogPath == nil || *record.LogPath != filepath.ToSlash(filepath.Join("logs", record.ProcessID+".log")) {
			return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: invalid detached log path")
		}
	}
	return nil
}

func (control Control) Validate() error {
	if control.ControlVersion != 1 || !validProcessID(control.ProcessID) {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-SCHEMA: invalid control record")
	}
	parsed, err := url.Parse(control.Endpoint)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-SCHEMA: endpoint is not loopback HTTP")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 || net.ParseIP(parsed.Hostname()) == nil {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-SCHEMA: invalid endpoint port")
	}
	token, err := base64.RawURLEncoding.DecodeString(control.Token)
	if err != nil || len(token) != 32 {
		return fmt.Errorf("INGOT-PROCESS-CONTROL-SCHEMA: invalid token")
	}
	return nil
}

func (last LastExit) Validate() error {
	if last.ProcessVersion != 1 || !validProcessID(last.ProcessID) || last.RuntimeGeneration == 0 || last.Argv == nil || last.StartedAt.IsZero() {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid process record")
	}
	if last.Mode != "foreground" && last.Mode != "detached" {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid mode")
	}
	if last.SupervisorPID <= 0 || last.SupervisorBirthID == "" || !image.ValidDigest(last.ImageID) || !image.ValidDigest(last.ArtifactDigest) {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid identity")
	}
	if err := last.Target.Validate(); err != nil {
		return err
	}
	if last.RuntimePID == nil {
		if last.RuntimeBirthID != "" || (last.Reason != "startup_error" && last.Reason != "supervisor_error" && last.Reason != "unknown") {
			return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: missing runtime identity")
		}
	} else if *last.RuntimePID <= 0 || last.RuntimeBirthID == "" {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid runtime identity")
	}
	if err := validateArgv(last.Argv); err != nil {
		return err
	}
	if last.Mode == "foreground" && last.LogPath != nil {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: foreground process has a log path")
	}
	if last.Mode == "detached" && (last.LogPath == nil || *last.LogPath != filepath.ToSlash(filepath.Join("logs", last.ProcessID+".log"))) {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid detached log path")
	}
	if last.ExitedAt.IsZero() || last.ExitedAt.Before(last.StartedAt) || len(last.Error) > 512 {
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid exit metadata")
	}
	switch last.Reason {
	case "completed", "runtime_error", "signal", "startup_error", "supervisor_error", "unknown":
	default:
		return fmt.Errorf("INGOT-PROCESS-LAST-EXIT-SCHEMA: invalid reason")
	}
	return nil
}

func validateArgv(argv []string) error {
	for _, argument := range argv {
		if strings.ContainsRune(argument, 0) {
			return fmt.Errorf("INGOT-PROCESS-RECORD-SCHEMA: argv contains NUL")
		}
	}
	return nil
}

func validProcessID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, character := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return value[14] == '4' && strings.ContainsRune("89ab", rune(value[19]))
}

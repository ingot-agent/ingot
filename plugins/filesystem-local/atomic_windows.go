//go:build windows

package filesystemlocal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"unsafe"

	"golang.org/x/sys/windows"
)

type fileRenameInformation struct {
	flags          uint32
	rootDirectory  windows.Handle
	fileNameLength uint32
	fileName       [1]uint16
}

func atomicReplaceAt(root *os.File, source, destination string) error {
	return renameAt(root, source, destination, true)
}

func atomicRenameNoReplaceAt(root *os.File, source, destination string) error {
	err := renameAt(root, source, destination, false)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) || errors.Is(err, windows.ERROR_FILE_EXISTS) ||
		errors.Is(err, windows.STATUS_OBJECT_NAME_COLLISION) {
		return fmt.Errorf("destination exists: %w", fs.ErrExist)
	}
	return err
}

func renameAt(root *os.File, source, destination string, replace bool) error {
	sourceParent, sourceName, err := openParentHandleAt(root, source)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(sourceParent)
	destinationParent, destinationName, err := openParentHandleAt(root, destination)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(destinationParent)

	sourceHandle, err := openRelativeHandle(sourceParent, sourceName, windows.DELETE|windows.SYNCHRONIZE|windows.FILE_READ_ATTRIBUTES, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(sourceHandle)
	if err := rejectReparseHandle(sourceHandle); err != nil {
		return err
	}

	destinationUTF16, err := windows.UTF16FromString(destinationName)
	if err != nil {
		return err
	}
	nameBytes := (len(destinationUTF16) - 1) * 2
	var layout fileRenameInformation
	bufferSize := int(unsafe.Offsetof(layout.fileName)) + nameBytes
	buffer := make([]byte, bufferSize)
	information := (*fileRenameInformation)(unsafe.Pointer(&buffer[0]))
	if replace {
		information.flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	}
	information.rootDirectory = destinationParent
	information.fileNameLength = uint32(nameBytes)
	copy((*[windows.MAX_LONG_PATH]uint16)(unsafe.Pointer(&information.fileName[0]))[:nameBytes/2:nameBytes/2], destinationUTF16)
	return windows.NtSetInformationFile(
		sourceHandle,
		&windows.IO_STATUS_BLOCK{},
		&buffer[0],
		uint32(bufferSize),
		windows.FileRenameInformation,
	)
}

func openParentHandleAt(root *os.File, logical string) (windows.Handle, string, error) {
	parent := path.Dir(logical)
	segments, err := logicalSegments(parent)
	if err != nil {
		return windows.InvalidHandle, "", err
	}
	process := windows.CurrentProcess()
	var current windows.Handle
	if err := windows.DuplicateHandle(
		process,
		windows.Handle(root.Fd()),
		process,
		&current,
		0,
		false,
		windows.DUPLICATE_SAME_ACCESS,
	); err != nil {
		return windows.InvalidHandle, "", err
	}
	for _, segment := range segments {
		next, openErr := openRelativeHandle(current, segment, windows.FILE_GENERIC_READ, windows.FILE_DIRECTORY_FILE)
		_ = windows.CloseHandle(current)
		if openErr != nil {
			return windows.InvalidHandle, "", openErr
		}
		if reparseErr := rejectReparseHandle(next); reparseErr != nil {
			_ = windows.CloseHandle(next)
			return windows.InvalidHandle, "", reparseErr
		}
		current = next
	}
	return current, path.Base(logical), nil
}

func openRelativeHandle(parent windows.Handle, name string, access, options uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	attributes := &windows.OBJECT_ATTRIBUTES{
		Length:        uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: parent,
		ObjectName:    objectName,
		Attributes:    windows.OBJ_CASE_INSENSITIVE,
	}
	var handle windows.Handle
	err = windows.NtCreateFile(
		&handle,
		access,
		attributes,
		&windows.IO_STATUS_BLOCK{},
		nil,
		windows.FILE_ATTRIBUTE_NORMAL,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN,
		options|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT,
		0,
		0,
	)
	return handle, err
}

func rejectReparseHandle(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: %w", ErrSymlinkUnsupported, fs.ErrPermission)
	}
	return nil
}

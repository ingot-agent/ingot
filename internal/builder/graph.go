package builder

import (
	"context"
	"fmt"
	"go/types"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

type Cardinality string

const (
	CardinalityOne      Cardinality = "ONE"
	CardinalityOptional Cardinality = "OPTIONAL"
	CardinalityMany     Cardinality = "MANY"
)

type Graph struct {
	Components    []*Component
	CreationOrder []*Component
}

type Component struct {
	ID             string
	PluginID       string
	PluginName     string
	DirectIndex    int
	ComponentIndex int
	ImportPath     string
	PackageName    string
	ConfigImport   string
	Package        *packages.Package
	ConfigType     *types.Named
	Dependencies   *types.Named
	Exports        *types.Named
	DependencyList []*Dependency
	ExportList     []*Export
}

type Dependency struct {
	Name        string
	Type        types.Type
	TypeString  string
	Cardinality Cardinality
	Target      types.Type
	WrapperSDK  string
	Providers   []Provider
}

type Export struct {
	Name       string
	Type       types.Type
	TypeString string
	Index      int
}

type Provider struct {
	Component *Component
	Export    *Export
	Flatten   bool
}

type LoadOptions struct{ GOMODCACHE string }

// LoadGraph loads all locked Component contracts with go/packages, validates
// their exact public shape, resolves capability providers, and computes the
// deterministic Component creation order.
func LoadGraph(ctx context.Context, rootDirectory string, lock *Lock, options LoadOptions) (*Graph, error) {
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	imports := []string{"context"}
	sdkModules := make(map[string]bool, len(lock.SDKs))
	for _, sdk := range lock.SDKs {
		imports = append(imports, sdk.ModulePath)
		sdkModules[sdk.ModulePath] = true
	}
	type componentLocation struct {
		plugin        LockedPlugin
		component     LockedComponent
		direct, index int
		importPath    string
	}
	locations := make([]componentLocation, 0)
	for directIndex, plugin := range lock.Plugins {
		imports = append(imports, joinImport(plugin.ID, plugin.RootPackage))
		for componentIndex, component := range plugin.Components {
			importPath := joinImport(plugin.ID, component.Package)
			imports = append(imports, importPath)
			locations = append(locations, componentLocation{plugin: plugin, component: component, direct: directIndex, index: componentIndex, importPath: importPath})
		}
	}
	imports = uniqueStrings(imports)
	environment := lockedEnvironment(lock, options.GOMODCACHE)
	buildFlags := []string{"-mod=readonly"}
	if len(lock.Build.Tags) > 0 {
		buildFlags = append(buildFlags, "-tags="+strings.Join(lock.Build.Tags, ","))
	}
	configuration := &packages.Config{Context: ctx, Dir: rootDirectory, Env: environment, Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax | packages.NeedModule, BuildFlags: buildFlags}
	loaded, err := packages.Load(configuration, imports...)
	if err != nil {
		return nil, &Error{Code: "INGOT-PACKAGES-LOAD", Path: rootDirectory, Err: err}
	}
	byPath := map[string]*packages.Package{}
	var loadErrors []string
	for _, pkg := range loaded {
		byPath[pkg.PkgPath] = pkg
		for _, packageErr := range pkg.Errors {
			loadErrors = append(loadErrors, packageErr.Error())
		}
	}
	if len(loadErrors) > 0 {
		return nil, &Error{Code: "INGOT-PACKAGES-ERROR", Path: rootDirectory, Err: fmt.Errorf("%s", strings.Join(loadErrors, "\n"))}
	}
	contextPackage := byPath["context"]
	if contextPackage == nil {
		return nil, &Error{Code: "INGOT-PACKAGES-FOUNDATION", Want: "context and all configured SDK modules"}
	}
	cleanupTypes := make(map[string]*types.Named, len(lock.SDKs))
	for _, sdk := range lock.SDKs {
		sdkPackage := byPath[sdk.ModulePath]
		if sdkPackage == nil {
			return nil, &Error{Code: "INGOT-PACKAGES-FOUNDATION", Want: sdk.ModulePath}
		}
		cleanupType, typeErr := namedType(sdkPackage, "Cleanup")
		if typeErr != nil {
			return nil, typeErr
		}
		cleanupTypes[sdk.ModulePath] = cleanupType
	}
	primaryCleanup := cleanupTypes[lock.SDKs[0].ModulePath]
	for index, sdk := range lock.SDKs[1:] {
		if !types.ConvertibleTo(cleanupTypes[sdk.ModulePath], primaryCleanup) {
			return nil, &Error{Code: "INGOT-SDK-CLEANUP-INCOMPATIBLE", Field: fmt.Sprintf("sdks[%d]", index+1), Actual: types.TypeString(cleanupTypes[sdk.ModulePath], packageQualifier), Want: "Cleanup convertible to the primary SDK Cleanup"}
		}
	}
	contextType, err := namedType(contextPackage, "Context")
	if err != nil {
		return nil, err
	}
	implementationPackages := map[string]bool{}
	for _, location := range locations {
		implementationPackages[location.importPath] = true
	}
	graph := &Graph{Components: make([]*Component, len(locations))}
	for i, location := range locations {
		componentPackage := byPath[location.importPath]
		configPackage := byPath[joinImport(location.plugin.ID, location.plugin.RootPackage)]
		if componentPackage == nil || configPackage == nil {
			return nil, &Error{Code: "INGOT-COMPONENT-PACKAGE", Plugin: location.plugin.ID, Actual: location.importPath}
		}
		configType, typeErr := namedStruct(configPackage, "Config", "INGOT-CONFIG-CONTRACT")
		if typeErr != nil {
			return nil, typeErr
		}
		dependencies, typeErr := namedStruct(componentPackage, "Dependencies", "INGOT-COMPONENT-CONTRACT")
		if typeErr != nil {
			return nil, typeErr
		}
		exports, typeErr := namedStruct(componentPackage, "Exports", "INGOT-COMPONENT-CONTRACT")
		if typeErr != nil {
			return nil, typeErr
		}
		component := &Component{ID: location.plugin.ID + "/" + location.component.Name, PluginID: location.plugin.ID, PluginName: location.plugin.Name, DirectIndex: location.direct, ComponentIndex: location.index,
			ImportPath: location.importPath, PackageName: componentPackage.Name, ConfigImport: configPackage.PkgPath, Package: componentPackage, ConfigType: configType, Dependencies: dependencies, Exports: exports}
		if err := validateFields(component, dependencies, true, implementationPackages, sdkModules); err != nil {
			return nil, err
		}
		if err := validateFields(component, exports, false, implementationPackages, sdkModules); err != nil {
			return nil, err
		}
		if err := validateNew(component, contextType, configType, dependencies, exports, cleanupTypes); err != nil {
			return nil, err
		}
		graph.Components[i] = component
	}
	if err := graph.resolve(); err != nil {
		return nil, err
	}
	return graph, nil
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			result = append(result, value)
			seen[value] = true
		}
	}
	return result
}
func joinImport(modulePath, relative string) string {
	if relative == "." {
		return modulePath
	}
	return modulePath + "/" + strings.TrimPrefix(relative, "./")
}

func namedType(pkg *packages.Package, name string) (*types.Named, error) {
	object := pkg.Types.Scope().Lookup(name)
	if object == nil {
		return nil, fmt.Errorf("package %s has no exported %s", pkg.PkgPath, name)
	}
	typeValue := types.Unalias(object.Type())
	named, ok := typeValue.(*types.Named)
	if !ok {
		return nil, fmt.Errorf("%s.%s is not a named type", pkg.PkgPath, name)
	}
	return named, nil
}

func namedStruct(pkg *packages.Package, name, code string) (*types.Named, error) {
	named, err := namedType(pkg, name)
	if err != nil {
		return nil, &Error{Code: code, Plugin: pkg.PkgPath, Field: name, Err: err}
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil, &Error{Code: code, Plugin: pkg.PkgPath, Field: name, Want: "exported named struct", Actual: types.TypeString(named, nil)}
	}
	return named, nil
}

func validateFields(component *Component, named *types.Named, dependencies bool, implementationPackages, sdkModules map[string]bool) error {
	structure := named.Underlying().(*types.Struct)
	for i := 0; i < structure.NumFields(); i++ {
		field := structure.Field(i)
		kind := "Exports"
		if dependencies {
			kind = "Dependencies"
		}
		if !field.Exported() || field.Embedded() {
			return &Error{Code: "INGOT-COMPONENT-FIELD", Plugin: component.ID, Field: kind + "." + field.Name(), Want: "top-level named exported non-embedded field"}
		}
		typeString := types.TypeString(field.Type(), packageQualifier)
		if dependencies {
			cardinality, target, wrapperSDK, err := classifyDependency(field.Type(), sdkModules)
			if err != nil {
				return &Error{Code: "INGOT-CAPABILITY-TYPE", Plugin: component.ID, Field: "Dependencies." + field.Name(), Actual: typeString, Err: err}
			}
			base := capabilityBase(target, sdkModules)
			if err := validateCapabilityBase(base, implementationPackages); err != nil {
				return &Error{Code: "INGOT-CAPABILITY-TYPE", Plugin: component.ID, Field: "Dependencies." + field.Name(), Actual: typeString, Err: err}
			}
			component.DependencyList = append(component.DependencyList, &Dependency{Name: field.Name(), Type: field.Type(), TypeString: typeString, Cardinality: cardinality, Target: target, WrapperSDK: wrapperSDK})
		} else {
			component.ExportList = append(component.ExportList, &Export{Name: field.Name(), Type: field.Type(), TypeString: typeString, Index: i})
		}
	}
	return nil
}

func packageQualifier(pkg *types.Package) string {
	if pkg == nil {
		return ""
	}
	return pkg.Path()
}

func classifyDependency(value types.Type, sdkModules map[string]bool) (Cardinality, types.Type, string, error) {
	value = types.Unalias(value)
	if argument, sdkModule, ok := sdkWrapper(value, sdkModules, "Optional"); ok {
		return CardinalityOptional, argument, sdkModule, nil
	}
	if slice, ok := value.(*types.Slice); ok {
		return CardinalityMany, slice.Elem(), "", nil
	}
	if err := validateExpression(value, sdkModules); err != nil {
		return "", nil, "", err
	}
	return CardinalityOne, value, "", nil
}

func validateExpression(value types.Type, sdkModules map[string]bool) error {
	value = types.Unalias(value)
	if argument, _, ok := sdkWrapper(value, sdkModules, "Optional"); ok {
		return validateExpression(argument, sdkModules)
	}
	if argument, _, ok := sdkWrapper(value, sdkModules, "Named"); ok {
		return validateExpression(argument, sdkModules)
	}
	if slice, ok := value.(*types.Slice); ok {
		return validateExpression(slice.Elem(), sdkModules)
	}
	return nil
}

func sdkWrapper(value types.Type, sdkModules map[string]bool, name string) (types.Type, string, bool) {
	named, ok := types.Unalias(value).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil || !sdkModules[named.Obj().Pkg().Path()] || named.Obj().Name() != name || named.TypeArgs() == nil || named.TypeArgs().Len() != 1 {
		return nil, "", false
	}
	return named.TypeArgs().At(0), named.Obj().Pkg().Path(), true
}

func capabilityBase(value types.Type, sdkModules map[string]bool) types.Type {
	value = types.Unalias(value)
	if argument, _, ok := sdkWrapper(value, sdkModules, "Optional"); ok {
		return capabilityBase(argument, sdkModules)
	}
	if argument, _, ok := sdkWrapper(value, sdkModules, "Named"); ok {
		return capabilityBase(argument, sdkModules)
	}
	if slice, ok := value.(*types.Slice); ok {
		return capabilityBase(slice.Elem(), sdkModules)
	}
	return types.Unalias(value)
}

func validateCapabilityBase(value types.Type, implementationPackages map[string]bool) error {
	if pointer, ok := value.(*types.Pointer); ok {
		value = types.Unalias(pointer.Elem())
	}
	named, ok := value.(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil || !named.Obj().Exported() {
		return fmt.Errorf("base target must be a package-level exported named type or pointer to one")
	}
	if named.Obj().Pkg().Path() == "builtin" || named.Obj().Name() == "error" {
		return fmt.Errorf("builtin, any, and bare error are not stable capability targets")
	}
	if implementationPackages[named.Obj().Pkg().Path()] {
		return fmt.Errorf("base target is declared by graph Component implementation package %s", named.Obj().Pkg().Path())
	}
	return nil
}

func validateNew(component *Component, contextType, configType, dependencies, exports *types.Named, cleanupTypes map[string]*types.Named) error {
	object := component.Package.Types.Scope().Lookup("New")
	function, ok := object.(*types.Func)
	if !ok || !object.Exported() {
		return &Error{Code: "INGOT-COMPONENT-NEW", Plugin: component.ID, Field: "New", Want: "exported function"}
	}
	signature, ok := function.Type().(*types.Signature)
	if !ok || signature.Variadic() || signature.Params().Len() != 3 || signature.Results().Len() != 3 {
		return &Error{Code: "INGOT-COMPONENT-NEW", Plugin: component.ID, Field: "New", Want: "func(context.Context, Config, Dependencies) (Exports, sdk.Cleanup, error)", Actual: types.TypeString(function.Type(), packageQualifier)}
	}
	wants := []types.Type{contextType, configType, dependencies, exports, nil, types.Universe.Lookup("error").Type()}
	actual := []types.Type{signature.Params().At(0).Type(), signature.Params().At(1).Type(), signature.Params().At(2).Type(), signature.Results().At(0).Type(), signature.Results().At(1).Type(), signature.Results().At(2).Type()}
	for i := range wants {
		if i == 4 {
			validCleanup := false
			for _, cleanupType := range cleanupTypes {
				if types.Identical(actual[i], cleanupType) {
					validCleanup = true
					break
				}
			}
			if validCleanup {
				continue
			}
			return &Error{Code: "INGOT-COMPONENT-NEW", Plugin: component.ID, Field: "New", Want: "Cleanup from one configured SDK", Actual: types.TypeString(function.Type(), packageQualifier)}
		}
		if !types.Identical(actual[i], wants[i]) {
			return &Error{Code: "INGOT-COMPONENT-NEW", Plugin: component.ID, Field: "New", Want: "exact Component constructor signature", Actual: types.TypeString(function.Type(), packageQualifier)}
		}
	}
	return nil
}

func (graph *Graph) resolve() error {
	adjacency := make([]map[int]bool, len(graph.Components))
	indegree := make([]int, len(graph.Components))
	for i := range adjacency {
		adjacency[i] = map[int]bool{}
	}
	componentIndex := map[*Component]int{}
	for i, component := range graph.Components {
		componentIndex[component] = i
	}
	for consumerIndex, consumer := range graph.Components {
		for _, dependency := range consumer.DependencyList {
			var providers []Provider
			for providerIndex, providerComponent := range graph.Components {
				for _, export := range providerComponent.ExportList {
					matches, flatten := matchesProvider(export.Type, dependency.Target, dependency.Cardinality)
					if !matches {
						continue
					}
					if providerIndex == consumerIndex {
						return &Error{Code: "INGOT-GRAPH-SELF-LOOP", Plugin: consumer.ID, Field: "Dependencies." + dependency.Name, Actual: "Exports." + export.Name}
					}
					providers = append(providers, Provider{Component: providerComponent, Export: export, Flatten: flatten})
				}
			}
			if dependency.Cardinality == CardinalityOne && len(providers) != 1 {
				return providerCountError(consumer, dependency, providers, "exactly 1")
			}
			if dependency.Cardinality == CardinalityOptional && len(providers) > 1 {
				return providerCountError(consumer, dependency, providers, "0 or 1")
			}
			dependency.Providers = providers
			for _, provider := range providers {
				providerIndex := componentIndex[provider.Component]
				if !adjacency[providerIndex][consumerIndex] {
					adjacency[providerIndex][consumerIndex] = true
					indegree[consumerIndex]++
				}
			}
		}
	}
	order := deterministicTopological(graph.Components, adjacency, indegree)
	if len(order) != len(graph.Components) {
		cycle := findCycle(graph.Components, adjacency)
		return &Error{Code: "INGOT-GRAPH-CYCLE", Actual: strings.Join(cycle, " -> ")}
	}
	graph.CreationOrder = make([]*Component, len(order))
	rank := map[*Component]int{}
	for i, index := range order {
		graph.CreationOrder[i] = graph.Components[index]
		rank[graph.Components[index]] = i
	}
	for _, component := range graph.Components {
		for _, dependency := range component.DependencyList {
			sort.SliceStable(dependency.Providers, func(i, j int) bool {
				left, right := dependency.Providers[i], dependency.Providers[j]
				if rank[left.Component] != rank[right.Component] {
					return rank[left.Component] < rank[right.Component]
				}
				return left.Export.Index < right.Export.Index
			})
		}
	}
	return nil
}

func matchesProvider(source, target types.Type, cardinality Cardinality) (bool, bool) {
	if cardinality != CardinalityMany {
		return types.AssignableTo(source, target), false
	}
	if types.AssignableTo(source, target) {
		return true, false
	}
	if slice, ok := types.Unalias(source).(*types.Slice); ok && types.AssignableTo(slice.Elem(), target) {
		return true, true
	}
	return false, false
}

func providerCountError(component *Component, dependency *Dependency, providers []Provider, want string) error {
	names := make([]string, len(providers))
	for i, provider := range providers {
		names[i] = provider.Component.ID + ".Exports." + provider.Export.Name
	}
	return &Error{Code: "INGOT-GRAPH-PROVIDER-COUNT", Plugin: component.ID, Field: "Dependencies." + dependency.Name, Want: want, Actual: fmt.Sprintf("%d [%s]", len(providers), strings.Join(names, ", "))}
}

func deterministicTopological(components []*Component, adjacency []map[int]bool, sourceIndegree []int) []int {
	indegree := append([]int(nil), sourceIndegree...)
	ready := []int{}
	for i, value := range indegree {
		if value == 0 {
			ready = append(ready, i)
		}
	}
	var order []int
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool {
			left, right := components[ready[i]], components[ready[j]]
			if left.DirectIndex != right.DirectIndex {
				return left.DirectIndex < right.DirectIndex
			}
			return left.ComponentIndex < right.ComponentIndex
		})
		current := ready[0]
		ready = ready[1:]
		order = append(order, current)
		for next := range adjacency[current] {
			indegree[next]--
			if indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	return order
}

func findCycle(components []*Component, adjacency []map[int]bool) []string {
	state := make([]uint8, len(components))
	stack := []int{}
	position := map[int]int{}
	var cycle []int
	var visit func(int) bool
	visit = func(current int) bool {
		state[current] = 1
		position[current] = len(stack)
		stack = append(stack, current)
		nexts := make([]int, 0, len(adjacency[current]))
		for next := range adjacency[current] {
			nexts = append(nexts, next)
		}
		sort.Ints(nexts)
		for _, next := range nexts {
			if state[next] == 0 && visit(next) {
				return true
			}
			if state[next] == 1 {
				cycle = append([]int{}, stack[position[next]:]...)
				cycle = append(cycle, next)
				return true
			}
		}
		stack = stack[:len(stack)-1]
		delete(position, current)
		state[current] = 2
		return false
	}
	for i := range components {
		if state[i] == 0 && visit(i) {
			break
		}
	}
	result := make([]string, len(cycle))
	for i, index := range cycle {
		result[i] = components[index].ID
	}
	return result
}

func lockedEnvironment(lock *Lock, moduleCache string) []string {
	userHome, _ := os.UserHomeDir()
	if moduleCache == "" {
		goPath := os.Getenv("GOPATH")
		if goPath == "" {
			goPath = filepath.Join(userHome, "go")
		}
		moduleCache = filepath.Join(strings.Split(goPath, string(os.PathListSeparator))[0], "pkg", "mod")
	}
	cacheRoot, _ := os.UserCacheDir()
	goCache := os.Getenv("GOCACHE")
	if goCache == "" {
		goCache = filepath.Join(cacheRoot, "go-build")
	}
	temporary := os.TempDir()
	replacements := map[string]string{"GOWORK": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "CGO_ENABLED": "0", "GOOS": lock.Target.GOOS, "GOARCH": lock.Target.GOARCH, "GOEXPERIMENT": strings.Join(lock.Target.GOExperiment, ","), "GOMODCACHE": moduleCache, "GOTMPDIR": temporary}
	for _, item := range lock.Target.Tuning {
		replacements[item.Key] = item.Value
	}
	environment := baseBuildEnvironment(runtime.GOOS, userHome, goCache, temporary)
	return replaceEnvironment(environment, replacements)
}

func baseBuildEnvironment(goos, userHome, goCache, temporary string) []string {
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + userHome, "GOCACHE=" + goCache, "GOENV=off"}
	if goos == "windows" {
		environment = append(environment, "TEMP="+temporary, "TMP="+temporary)
		// These variables are part of the Windows process environment contract
		// and may be needed by the Go toolchain and programs it launches.
		for _, key := range []string{"SystemRoot", "WINDIR", "ComSpec", "PATHEXT", "USERPROFILE", "HOMEDRIVE", "HOMEPATH"} {
			if value := os.Getenv(key); value != "" {
				environment = append(environment, key+"="+value)
			}
		}
	} else {
		environment = append(environment, "TMPDIR="+temporary)
	}
	return environment
}

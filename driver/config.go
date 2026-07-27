package driver

import (
	"reflect"
	"time"
)

// CommonConfig contains provider-independent process defaults shared by the
// built-in Driver configurations. It lives in the public Driver SPI package so
// provider Config values never expose internal engine implementation types.
//
// Provider packages own their concrete Config types and explicitly translate
// every field into their private execution representation.
type CommonConfig struct {
	Command                 string
	CWD                     string
	Env                     []EnvBinding
	Instructions            *InstructionsBundleRef
	PromptTemplate          string
	BootstrapPromptTemplate string
	WorkspaceStrategy       *WorkspaceStrategy
	WorkspaceRuntime        *WorkspaceRuntimeConfig
	Timeout                 time.Duration
	GracePeriod             time.Duration
	ExtraArgs               []string
}

// WorkspaceStrategy describes the default workspace provisioning intent
// carried by a built-in Driver configuration. Call-scoped workspace options
// may replace it while resolving an invocation.
type WorkspaceStrategy struct {
	Type              WorkspaceStrategyType
	BaseRef           string
	BranchTemplate    string
	WorktreeParentDir string
}

// WorkspaceRuntimeConfig declares runtime services associated with a Driver's
// default workspace configuration.
type WorkspaceRuntimeConfig struct {
	Services []RuntimeServiceSpec
}

// Clone returns a deep copy of c. Built-in provider constructors use it to
// take an immutable construction-time snapshot: callers may safely reuse or
// mutate the slices, maps, and pointed-to values they used to build Config.
func (c CommonConfig) Clone() CommonConfig {
	out := c
	if c.Env != nil {
		out.Env = append([]EnvBinding{}, c.Env...)
	}
	if c.ExtraArgs != nil {
		out.ExtraArgs = append([]string{}, c.ExtraArgs...)
	}
	out.Instructions = cloneInstructionsBundleRef(c.Instructions)
	out.WorkspaceStrategy = cloneWorkspaceStrategy(c.WorkspaceStrategy)
	out.WorkspaceRuntime = cloneWorkspaceRuntimeConfig(c.WorkspaceRuntime)
	return out
}

func cloneInstructionsBundleRef(ref *InstructionsBundleRef) *InstructionsBundleRef {
	if ref == nil {
		return nil
	}
	out := *ref
	out.Native = cloneNativeMap(ref.Native)
	return &out
}

func cloneWorkspaceStrategy(strategy *WorkspaceStrategy) *WorkspaceStrategy {
	if strategy == nil {
		return nil
	}
	out := *strategy
	return &out
}

func cloneWorkspaceRuntimeConfig(config *WorkspaceRuntimeConfig) *WorkspaceRuntimeConfig {
	if config == nil {
		return nil
	}
	out := &WorkspaceRuntimeConfig{Services: make([]RuntimeServiceSpec, len(config.Services))}
	for i, service := range config.Services {
		out.Services[i] = service
		out.Services[i].Metadata = cloneStringMap(service.Metadata)
	}
	return out
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// cloneNativeMap copies the provider escape hatch recursively while retaining
// concrete collection and scalar types. Avoiding a JSON round trip prevents
// integer coercion and supports provider-specific typed collections.
func cloneNativeMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = cloneNativeValue(value)
	}
	return out
}

func cloneNativeValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneNativeReflect(reflect.ValueOf(value)).Interface()
}

func cloneNativeReflect(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type()).Elem()
		out.Set(cloneNativeReflect(value.Elem()))
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			out.SetMapIndex(iter.Key(), cloneNativeReflect(iter.Value()))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneNativeReflect(value.Index(i)))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			out.Index(i).Set(cloneNativeReflect(value.Index(i)))
		}
		return out
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneNativeReflect(value.Elem()))
		return out
	case reflect.Struct:
		out := reflect.New(value.Type()).Elem()
		out.Set(value)
		for i := 0; i < value.NumField(); i++ {
			if out.Field(i).CanSet() && value.Type().Field(i).IsExported() {
				out.Field(i).Set(cloneNativeReflect(value.Field(i)))
			}
		}
		return out
	default:
		return value
	}
}

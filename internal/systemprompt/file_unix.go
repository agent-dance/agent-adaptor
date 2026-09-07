//go:build !windows

package systemprompt

import "os"

func unsafeInfo(info os.FileInfo) bool { return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 }

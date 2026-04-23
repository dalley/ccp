package paths

import "os"

func statDir(d string) (os.FileInfo, error) { return os.Stat(d) }

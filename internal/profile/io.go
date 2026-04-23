package profile

import "io"

// copyReader exists so tests can swap the implementation if needed; it's a
// direct wrapper over io.Copy for now.
func copyReader(dst io.Writer, src io.Reader) (int64, error) {
	return io.Copy(dst, src)
}

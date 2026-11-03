package processx

import (
	"os"
	"sync"
)

// PipeFile owns exactly one os.File. Borrow File only for exec.Cmd's standard
// streams, which do not take ownership of assigned *os.Files. All closes,
// including concurrent shutdown and deferred cleanup, go through Close.
type PipeFile struct {
	*os.File
	once       sync.Once
	err        error
	afterClose func(*os.File, error)
}

// NewPipe creates two independent owners, without wrapping or duplicating an
// existing descriptor. afterClose observes each owner's sole File.Close call.
func NewPipe(afterClose func(*os.File, error)) (r, w *PipeFile, err error) {
	rf, wf, err := os.Pipe()
	if err != nil {
		return nil, nil, err
	}
	return &PipeFile{File: rf, afterClose: afterClose}, &PipeFile{File: wf, afterClose: afterClose}, nil
}

func (f *PipeFile) Close() error {
	f.once.Do(func() {
		f.err = f.File.Close()
		if f.afterClose != nil {
			f.afterClose(f.File, f.err)
		}
	})
	return f.err
}

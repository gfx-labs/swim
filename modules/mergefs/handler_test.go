package mergefs_test

import (
	"io/fs"
	"testing"

	"github.com/gfx-labs/swim/modules/mergefs"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func newMergefs(layers []afero.Fs) *mergefs.Mergefs {
	fsLayers := make([]fs.FS, len(layers))
	for i, l := range layers {
		fsLayers[i] = afero.NewIOFS(l)
	}
	m := new(mergefs.Mergefs)
	m.BuildLayers(fsLayers)
	return m
}

func TestMergeOverlayPriority(t *testing.T) {
	// layer 0 (high priority) has index.html with "top"
	// layer 1 (low priority) has index.html with "bottom"
	top := afero.NewMemMapFs()
	afero.WriteFile(top, "index.html", []byte("top"), 0o644)

	bottom := afero.NewMemMapFs()
	afero.WriteFile(bottom, "index.html", []byte("bottom"), 0o644)

	m := newMergefs([]afero.Fs{top, bottom})

	data, err := fs.ReadFile(m, "index.html")
	require.NoError(t, err)
	require.Equal(t, "top", string(data))
}

func TestMergeFallthrough(t *testing.T) {
	// layer 0 has only "a.txt"
	// layer 1 has only "b.txt"
	// both should be accessible
	layer0 := afero.NewMemMapFs()
	afero.WriteFile(layer0, "a.txt", []byte("aaa"), 0o644)

	layer1 := afero.NewMemMapFs()
	afero.WriteFile(layer1, "b.txt", []byte("bbb"), 0o644)

	m := newMergefs([]afero.Fs{layer0, layer1})

	data, err := fs.ReadFile(m, "a.txt")
	require.NoError(t, err)
	require.Equal(t, "aaa", string(data))

	data, err = fs.ReadFile(m, "b.txt")
	require.NoError(t, err)
	require.Equal(t, "bbb", string(data))
}

func TestMergeDirectoryListing(t *testing.T) {
	// layer 0 has static/app.js
	// layer 1 has static/style.css
	// reading static/ should list both files
	layer0 := afero.NewMemMapFs()
	layer0.MkdirAll("static", 0o755)
	afero.WriteFile(layer0, "static/app.js", []byte("js"), 0o644)

	layer1 := afero.NewMemMapFs()
	layer1.MkdirAll("static", 0o755)
	afero.WriteFile(layer1, "static/style.css", []byte("css"), 0o644)

	m := newMergefs([]afero.Fs{layer0, layer1})

	entries, err := fs.ReadDir(m, "static")
	require.NoError(t, err)

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name()] = true
	}
	require.True(t, names["app.js"], "expected app.js in merged listing")
	require.True(t, names["style.css"], "expected style.css in merged listing")
}

func TestMergeNotFound(t *testing.T) {
	layer0 := afero.NewMemMapFs()
	afero.WriteFile(layer0, "exists.txt", []byte("yes"), 0o644)

	m := newMergefs([]afero.Fs{layer0})

	_, err := m.Open("nope.txt")
	require.Error(t, err)
}

func TestMergeThreeLayers(t *testing.T) {
	tests := []struct {
		name     string
		file     string
		expected string
	}{
		{"highest priority wins", "shared.txt", "top"},
		{"middle layer unique file", "mid.txt", "middle"},
		{"bottom layer unique file", "base.txt", "base"},
	}

	top := afero.NewMemMapFs()
	afero.WriteFile(top, "shared.txt", []byte("top"), 0o644)

	mid := afero.NewMemMapFs()
	afero.WriteFile(mid, "shared.txt", []byte("mid"), 0o644)
	afero.WriteFile(mid, "mid.txt", []byte("middle"), 0o644)

	base := afero.NewMemMapFs()
	afero.WriteFile(base, "shared.txt", []byte("base"), 0o644)
	afero.WriteFile(base, "base.txt", []byte("base"), 0o644)

	m := newMergefs([]afero.Fs{top, mid, base})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := fs.ReadFile(m, tt.file)
			require.NoError(t, err)
			require.Equal(t, tt.expected, string(data))
		})
	}
}

func TestMergeOpenStripsSlashes(t *testing.T) {
	layer := afero.NewMemMapFs()
	afero.WriteFile(layer, "index.html", []byte("hello"), 0o644)

	m := newMergefs([]afero.Fs{layer})

	data, err := fs.ReadFile(m, "/index.html")
	require.NoError(t, err)
	require.Equal(t, "hello", string(data))
}

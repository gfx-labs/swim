package mergefs_test

import (
	"io/fs"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

// buildMergedFs mirrors the union construction in Mergefs.Provision:
// layers[0] is highest priority, layers[n-1] is lowest.
func buildMergedFs(layers []afero.Fs) afero.Fs {
	merged := layers[len(layers)-1]
	for i := len(layers) - 2; i >= 0; i-- {
		merged = afero.NewCopyOnWriteFs(merged, layers[i])
	}
	return afero.NewReadOnlyFs(merged)
}

func TestMergeOverlayPriority(t *testing.T) {
	// layer 0 (high priority) has index.html with "top"
	// layer 1 (low priority) has index.html with "bottom"
	top := afero.NewMemMapFs()
	afero.WriteFile(top, "index.html", []byte("top"), 0o644)

	bottom := afero.NewMemMapFs()
	afero.WriteFile(bottom, "index.html", []byte("bottom"), 0o644)

	merged := buildMergedFs([]afero.Fs{top, bottom})
	iofs := afero.NewIOFS(merged)

	data, err := fs.ReadFile(iofs, "index.html")
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

	merged := buildMergedFs([]afero.Fs{layer0, layer1})
	iofs := afero.NewIOFS(merged)

	data, err := fs.ReadFile(iofs, "a.txt")
	require.NoError(t, err)
	require.Equal(t, "aaa", string(data))

	data, err = fs.ReadFile(iofs, "b.txt")
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

	merged := buildMergedFs([]afero.Fs{layer0, layer1})
	iofs := afero.NewIOFS(merged)

	entries, err := fs.ReadDir(iofs, "static")
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

	merged := buildMergedFs([]afero.Fs{layer0})
	iofs := afero.NewIOFS(merged)

	_, err := iofs.Open("nope.txt")
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

	merged := buildMergedFs([]afero.Fs{top, mid, base})
	iofs := afero.NewIOFS(merged)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := fs.ReadFile(iofs, tt.file)
			require.NoError(t, err)
			require.Equal(t, tt.expected, string(data))
		})
	}
}

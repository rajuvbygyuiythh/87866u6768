package reply

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"thundr/internal/store"
)

// Folder Sequences mode: a folder of .txt files, each file = one sequence,
// one line = one step. Loaded via the dashboard ("Load Folder"), persisted
// to loaded_sequences.json so the selection survives restarts.

var folderCache = struct {
	sync.Mutex
	dir  string // store dir the cache belongs to
	seqs [][]string
}{}

// LoadFolderSequences reads every .txt in path and remembers the sequences.
func LoadFolderSequences(st *store.Store, path string) (int, error) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}
	var seqs [][]string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(path, e.Name()))
		if err != nil {
			continue
		}
		var lines []string
		for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			seqs = append(seqs, lines)
		}
	}
	folderCache.Lock()
	folderCache.dir = st.Dir
	folderCache.seqs = seqs
	folderCache.Unlock()
	_ = st.WriteJSON("loaded_sequences.json", seqs)
	return len(seqs), nil
}

// CleanFolderSequences forgets every loaded sequence.
func CleanFolderSequences(st *store.Store) {
	folderCache.Lock()
	folderCache.dir = st.Dir
	folderCache.seqs = nil
	folderCache.Unlock()
	_ = st.WriteJSON("loaded_sequences.json", [][]string{})
}

// FolderSequences returns the loaded sequences (memory first, disk second).
func FolderSequences(st *store.Store) [][]string {
	folderCache.Lock()
	if folderCache.dir == st.Dir && folderCache.seqs != nil {
		seqs := folderCache.seqs
		folderCache.Unlock()
		return seqs
	}
	folderCache.Unlock()
	var loaded [][]string
	if st.ReadJSON("loaded_sequences.json", &loaded) && len(loaded) > 0 {
		folderCache.Lock()
		folderCache.dir = st.Dir
		folderCache.seqs = loaded
		folderCache.Unlock()
		return loaded
	}
	return nil
}

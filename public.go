package mega

import (
	"context"
	"crypto/aes"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strings"
	"time"
)

// PublicLink is a parsed public URL. Its decryption key is deliberately private.
type PublicLink struct {
	Handle         string
	Folder         bool
	SelectedHandle string
	SelectedFile   bool
	key            []byte
}

// String and GoString prevent accidental formatting of a link's decryption key.
func (link PublicLink) String() string {
	kind := "file"
	if link.Folder {
		kind = "folder"
	}
	return "MEGA public " + kind + " " + link.Handle
}
func (link PublicLink) GoString() string { return link.String() }

// ParsePublicLink accepts modern and legacy mega.nz/mega.co.nz links.
// Invalid-link errors never include the supplied URL or key.
func ParsePublicLink(raw string) (PublicLink, error) {
	var link PublicLink
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Port() != "" || (strings.ToLower(u.Hostname()) != "mega.nz" && strings.ToLower(u.Hostname()) != "mega.co.nz") {
		return link, EARGS
	}
	fragment := u.Fragment
	query := u.Query()
	if i := strings.IndexByte(fragment, '?'); i >= 0 {
		values, e := url.ParseQuery(fragment[i+1:])
		if e != nil {
			return link, EARGS
		}
		for k, v := range values {
			query[k] = v
		}
		fragment = fragment[:i]
	}
	var encodedKey string
	path := strings.Split(strings.Trim(u.Path, "/"), "/")
	if strings.HasPrefix(fragment, "F!") || strings.HasPrefix(fragment, "!") {
		parts := strings.Split(fragment, "!")
		if len(parts) < 3 || len(parts) > 4 {
			return link, EARGS
		}
		link.Folder = parts[0] == "F"
		link.Handle = parts[1]
		encodedKey = parts[2]
		if len(parts) == 4 {
			if !link.Folder {
				return link, EARGS
			}
			link.SelectedHandle = parts[3]
		}
	} else {
		if len(path) != 2 || (path[0] != "file" && path[0] != "folder") {
			return link, EARGS
		}
		link.Folder = path[0] == "folder"
		link.Handle = path[1]
		parts := strings.Split(fragment, "/")
		encodedKey = parts[0]
		if len(parts) > 1 {
			if !link.Folder || len(parts) != 3 || (parts[1] != "file" && parts[1] != "folder") {
				return link, EARGS
			}
			link.SelectedHandle = parts[2]
			link.SelectedFile = parts[1] == "file"
		}
	}
	for _, name := range []string{"folder", "sub"} {
		if value := query.Get(name); value != "" {
			if !link.Folder || (link.SelectedHandle != "" && link.SelectedHandle != value) {
				return PublicLink{}, EARGS
			}
			link.SelectedHandle = value
		}
	}
	if !validHandle(link.Handle) || (link.SelectedHandle != "" && !validHandle(link.SelectedHandle)) {
		return PublicLink{}, EARGS
	}
	link.key, err = base64urldecode(strings.TrimRight(encodedKey, "="))
	want := 32
	if link.Folder {
		want = 16
	}
	if err != nil || len(link.key) != want {
		return PublicLink{}, EKEY
	}
	return link, nil
}

func validHandle(handle string) bool {
	decoded, err := base64urldecode(handle)
	return len(handle) == 8 && err == nil && len(decoded) == 6
}

// Entry is a relative-path metadata snapshot; it contains no encryption keys.
type Entry struct {
	Path     string
	Handle   string
	Size     int64
	IsFolder bool
}

// PublicResource is an immutable resolved public file or subtree.
// It is safe to download concurrently after client authentication/configuration.
type PublicResource struct {
	client  *Mega
	root    *Node
	entries []Entry
	nodes   []*Node
	size    int64
	folder  string
}

func (r *PublicResource) Name() string     { return r.root.name }
func (r *PublicResource) Size() int64      { return r.size }
func (r *PublicResource) IsFolder() bool   { return r.root.ntype == FOLDER }
func (r *PublicResource) Entries() []Entry { return append([]Entry(nil), r.entries...) }

func fileMeta(key []byte) (NodeMeta, error) {
	if len(key) != 32 {
		return NodeMeta{}, EKEY
	}
	aesKey := make([]byte, 16)
	for i := range aesKey {
		aesKey[i] = key[i] ^ key[i+16]
	}
	iv := make([]byte, 16)
	copy(iv, key[16:24])
	return NodeMeta{key: aesKey, compkey: append([]byte(nil), key...), iv: iv, mac: append([]byte(nil), key[24:32]...)}, nil
}

// ResolvePublic obtains metadata without downloading payloads or importing content.
func (m *Mega) ResolvePublic(ctx context.Context, raw string) (*PublicResource, error) {
	link, err := ParsePublicLink(raw)
	if err != nil {
		return nil, err
	}
	fs := newMegaFS()
	resource := &PublicResource{client: m}
	if !link.Folder {
		meta, err := fileMeta(link.key)
		if err != nil {
			return nil, err
		}
		payload, _ := json.Marshal([]DownloadMsg{{Cmd: "g", G: 1, P: link.Handle, SSL: 2}})
		body, err := m.apiRequest(ctx, payload, "")
		if err != nil {
			return nil, err
		}
		var response [1]DownloadResp
		if json.Unmarshal(body, &response) != nil {
			return nil, EBADRESP
		}
		if response[0].Err != 0 {
			return nil, parseError(response[0].Err)
		}
		if response[0].Size > math.MaxInt64 {
			return nil, EBADRESP
		}
		attr, err := decryptAttr(meta.key, response[0].Attr)
		if err != nil {
			return nil, err
		}
		resource.root = &Node{fs: fs, name: attr.Name, hash: link.Handle, ntype: FILE, size: int64(response[0].Size), meta: meta, publicHandle: link.Handle}
	} else {
		resource.folder = link.Handle
		payload, _ := json.Marshal([]FilesMsg{{Cmd: "f", C: 1}})
		body, err := m.apiRequest(ctx, payload, link.Handle)
		if err != nil {
			return nil, err
		}
		var response [1]FilesResp
		if json.Unmarshal(body, &response) != nil || len(response[0].F) == 0 {
			return nil, EBADRESP
		}
		block, _ := aes.NewCipher(link.key)
		parents := make(map[string]string, len(response[0].F))
		for _, encrypted := range response[0].F {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !validHandle(encrypted.Hash) || (encrypted.T != FILE && encrypted.T != FOLDER) || encrypted.Sz < 0 {
				return nil, EBADRESP
			}
			if _, exists := fs.lookup[encrypted.Hash]; exists {
				return nil, EBADRESP
			}
			var meta NodeMeta
			var attr FileAttr
			decoded := false
			for _, candidate := range strings.Split(encrypted.Key, "/") {
				_, encoded, ok := strings.Cut(candidate, ":")
				if !ok {
					continue
				}
				key, e := base64urldecode(encoded)
				want := 16
				if encrypted.T == FILE {
					want = 32
				}
				if e != nil || len(key) != want {
					continue
				}
				if blockDecrypt(block, key, key) != nil {
					continue
				}
				meta = NodeMeta{key: key, compkey: append([]byte(nil), key...)}
				if encrypted.T == FILE {
					meta, e = fileMeta(key)
					if e != nil {
						continue
					}
				}
				attr, e = decryptAttr(meta.key, encrypted.Attr)
				if e == nil {
					decoded = true
					break
				}
			}
			if !decoded {
				return nil, fmt.Errorf("public node decryption: %w", EKEY)
			}
			fs.lookup[encrypted.Hash] = &Node{fs: fs, name: attr.Name, hash: encrypted.Hash, ntype: encrypted.T, size: encrypted.Sz, ts: time.Unix(encrypted.Ts, 0), meta: meta, folderHandle: link.Handle}
			parents[encrypted.Hash] = encrypted.Parent
		}
		var roots []*Node
		for handle, node := range fs.lookup {
			if parent, ok := fs.lookup[parents[handle]]; ok {
				if parent.ntype != FOLDER {
					return nil, EBADRESP
				}
				node.parent = parent
				parent.children = append(parent.children, node)
			} else {
				roots = append(roots, node)
			}
		}
		if len(roots) != 1 || roots[0].ntype != FOLDER {
			return nil, EBADRESP
		}
		// Validate the full graph before narrowing it to a selected descendant.
		seen := make(map[*Node]bool)
		var validate func(*Node) error
		validate = func(n *Node) error {
			if seen[n] {
				return ECIRCULAR
			}
			seen[n] = true
			for _, c := range n.children {
				if err := validate(c); err != nil {
					return err
				}
			}
			return nil
		}
		if err := validate(roots[0]); err != nil {
			return nil, err
		}
		if len(seen) != len(fs.lookup) {
			return nil, ECIRCULAR
		}
		resource.root = roots[0]
		if link.SelectedHandle != "" {
			node, ok := fs.lookup[link.SelectedHandle]
			if !ok {
				return nil, ENOENT
			}
			if link.SelectedFile && node.ntype != FILE {
				return nil, EARGS
			}
			if !link.SelectedFile && node.ntype != FOLDER {
				return nil, EARGS
			}
			resource.root = node
		}
	}
	if err := resource.snapshot(); err != nil {
		return nil, err
	}
	return resource, nil
}

func (r *PublicResource) snapshot() error {
	var walk func(*Node, string) error
	walk = func(node *Node, parent string) error {
		if !safeName(node.name) {
			return fmt.Errorf("unsafe MEGA filename: %w", EARGS)
		}
		relative := node.name
		if parent != "" {
			relative = parent + "/" + relative
		}
		r.entries = append(r.entries, Entry{Path: relative, Handle: node.hash, Size: node.size, IsFolder: node.ntype == FOLDER})
		r.nodes = append(r.nodes, node)
		if node.ntype == FILE {
			if node.size > math.MaxInt64-r.size {
				return EBADRESP
			}
			r.size += node.size
		}
		sort.Slice(node.children, func(i, j int) bool { return node.children[i].name < node.children[j].name })
		used := make(map[string]bool)
		for _, child := range node.children {
			collision := strings.ToLower(child.name)
			if used[collision] {
				return fmt.Errorf("ambiguous MEGA filenames: %w", EEXIST)
			}
			used[collision] = true
			if err := walk(child, relative); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(r.root, "")
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return false
	}
	if strings.ContainsAny(name, "/\\:<>\"|?*") {
		return false
	}
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	if stem == "CON" || stem == "PRN" || stem == "AUX" || stem == "NUL" {
		return false
	}
	if len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && stem[3] >= '1' && stem[3] <= '9' {
		return false
	}
	return true
}

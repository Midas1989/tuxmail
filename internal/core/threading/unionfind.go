// SPDX-License-Identifier: AGPL-3.0-or-later

package threading

import "sort"

// UnionFind is a disjoint-set forest over string keys with path compression
// and union by size.
type UnionFind struct {
	parent map[string]string
	size   map[string]int
}

// NewUnionFind returns an empty UnionFind.
func NewUnionFind() *UnionFind {
	return &UnionFind{parent: map[string]string{}, size: map[string]int{}}
}

// Add inserts key as a singleton set if it is not present yet.
func (u *UnionFind) Add(key string) {
	if _, ok := u.parent[key]; !ok {
		u.parent[key] = key
		u.size[key] = 1
	}
}

// Find returns the representative of key's set (adding key if needed).
func (u *UnionFind) Find(key string) string {
	u.Add(key)
	root := key
	for u.parent[root] != root {
		root = u.parent[root]
	}
	for key != root {
		next := u.parent[key]
		u.parent[key] = root
		key = next
	}
	return root
}

// Union merges the sets of a and b.
func (u *UnionFind) Union(a, b string) {
	ra, rb := u.Find(a), u.Find(b)
	if ra == rb {
		return
	}
	if u.size[ra] < u.size[rb] {
		ra, rb = rb, ra
	}
	u.parent[rb] = ra
	u.size[ra] += u.size[rb]
}

// Sets returns all sets, each sorted, ordered by their smallest element.
func (u *UnionFind) Sets() [][]string {
	groups := map[string][]string{}
	for k := range u.parent {
		r := u.Find(k)
		groups[r] = append(groups[r], k)
	}
	out := make([][]string, 0, len(groups))
	for _, g := range groups {
		sort.Strings(g)
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i][0] < out[j][0] })
	return out
}

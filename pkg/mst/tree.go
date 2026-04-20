package mst

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/restayway/openbooking/pkg/protocol"
)

type Leaf struct {
	Key   string
	Value string
}

type ProofStep struct {
	Position string `json:"position"`
	Hash     string `json:"hash"`
}

type Tree struct {
	leaves []Leaf
	root   string
}

func New(records []protocol.Record) (*Tree, error) {
	leaves := make([]Leaf, 0, len(records))
	for _, record := range records {
		if err := record.ID.Validate(); err != nil {
			return nil, err
		}
		leaves = append(leaves, Leaf{
			Key:   record.ID.URI(),
			Value: record.Hash,
		})
	}
	sort.Slice(leaves, func(i, j int) bool {
		return leaves[i].Key < leaves[j].Key
	})
	tree := &Tree{leaves: leaves}
	tree.root = merkleRoot(leaves)
	return tree, nil
}

func (t *Tree) RootCID() string {
	return t.root
}

func (t *Tree) Leaves() []Leaf {
	out := make([]Leaf, len(t.leaves))
	copy(out, t.leaves)
	return out
}

func (t *Tree) Proof(key string) ([]ProofStep, error) {
	if len(t.leaves) == 0 {
		return nil, fmt.Errorf("empty tree")
	}
	level := leafHashes(t.leaves)
	index := -1
	for i, leaf := range t.leaves {
		if leaf.Key == key {
			index = i
			break
		}
	}
	if index == -1 {
		return nil, fmt.Errorf("key not found")
	}

	proof := make([]ProofStep, 0)
	for len(level) > 1 {
		if index%2 == 0 {
			if index+1 < len(level) {
				proof = append(proof, ProofStep{Position: "right", Hash: level[index+1]})
			}
		} else {
			proof = append(proof, ProofStep{Position: "left", Hash: level[index-1]})
		}

		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}
			next = append(next, hashStrings(left, right))
		}
		index /= 2
		level = next
	}
	return proof, nil
}

func VerifyProof(key, valueHash string, proof []ProofStep, root string) bool {
	current := hashStrings(key, valueHash)
	for _, step := range proof {
		if step.Position == "left" {
			current = hashStrings(step.Hash, current)
			continue
		}
		current = hashStrings(current, step.Hash)
	}
	return current == root
}

func leafHashes(leaves []Leaf) []string {
	level := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		level = append(level, hashStrings(leaf.Key, leaf.Value))
	}
	return level
}

func merkleRoot(leaves []Leaf) string {
	if len(leaves) == 0 {
		return hashStrings("openbooking", "empty")
	}
	level := leafHashes(leaves)
	for len(level) > 1 {
		next := make([]string, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			left := level[i]
			right := left
			if i+1 < len(level) {
				right = level[i+1]
			}
			next = append(next, hashStrings(left, right))
		}
		level = next
	}
	return level[0]
}

func hashStrings(parts ...string) string {
	sum := sha256.Sum256([]byte(fmt.Sprint(parts)))
	return hex.EncodeToString(sum[:])
}

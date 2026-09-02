// Package chain defines the core types shared by the follower, stores and
// decoders.
package chain

import "encoding/json"

// Block is one zone block reduced to its spine fields plus the raw JSON body.
// Height, Hash and ParentHash come from woHeader — the zone-level linkage in
// both the flat RPC shape and the WS newChainBlocksV2 shape.
type Block struct {
	Height     uint64
	Hash       string
	ParentHash string
	Raw        json.RawMessage
}

// Update is an atomic chain mutation: Rollback ordered tip→ancestor, Apply
// ordered ancestor→tip. Processing them in that order lands on the new
// canonical chain.
type Update struct {
	Rollback []Block
	Apply    []Block
}

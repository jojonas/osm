package augment

import (
	"context"
	"fmt"
	"sync"

	osm "github.com/paulmach/go.osm"
)

// A ChildFirstOrdering is a struct that allows for a set of relations to be
// processed in a dept first order. Since relations can reference other
// relations we need to make sure children are added before parents.
type ChildFirstOrdering struct {
	// CompletedIndex is the number of relation ids in the provided
	// array that have been finished. This can be used as a good restart position.
	CompletedIndex int

	ctx     context.Context
	done    func()
	ds      RelationDatasource
	visited map[osm.RelationID]struct{}
	out     chan osm.RelationID
	wg      sync.WaitGroup

	id  osm.RelationID
	err error
}

// NewChildFirstOrdering creates a new ordering object. It is used to provided
// a child before parent ordering for relations. This order must be used when
// inserting+augmenting relations into the datastore.
func NewChildFirstOrdering(
	ctx context.Context,
	ids []osm.RelationID,
	relations RelationDatasource,
) *ChildFirstOrdering {
	ctx, done := context.WithCancel(ctx)
	o := &ChildFirstOrdering{
		ctx:     ctx,
		done:    done,
		ds:      relations,
		visited: make(map[osm.RelationID]struct{}, len(ids)),
		out:     make(chan osm.RelationID),
	}

	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		defer close(o.out)

		path := make([]osm.RelationID, 0, 100)
		for i, id := range ids {
			err := o.walk(id, path)
			if err != nil {
				o.err = err
				return
			}

			o.CompletedIndex = i
		}
	}()

	return o
}

// Err returns a non-nil error if something went wrong with search,
// like a cycle, or a datasource error.
func (o *ChildFirstOrdering) Err() error {
	return o.err
}

// Next locates the next relation id that can be used.
// Returns false if the context is closed, something went wrong
// or the full tree has been walked.
func (o *ChildFirstOrdering) Next() bool {
	if o.err != nil {
		return false
	}

	select {
	case id := <-o.out:
		if id == 0 {
			return false
		}
		o.id = id
		return true
	case <-o.ctx.Done():
		o.err = o.ctx.Err()
		return false
	}
}

// RelationID is the id found by the previous scan.
func (o *ChildFirstOrdering) RelationID() osm.RelationID {
	return o.id
}

// Close can be used to terminate the scanning process before
// all ids have been walked.
func (o *ChildFirstOrdering) Close() {
	o.done()
	o.wg.Wait()
}

func (o *ChildFirstOrdering) walk(id osm.RelationID, path []osm.RelationID) error {
	if _, ok := o.visited[id]; ok {
		return nil
	}

	relations, err := o.ds.RelationHistory(o.ctx, id)
	if err != nil {
		return err
	}

	for _, r := range relations {
		for _, m := range r.Members {
			if m.Type != osm.RelationType {
				continue
			}

			mid := osm.RelationID(m.Ref)
			for _, pid := range path {
				if pid == mid {
					return fmt.Errorf("augment: relation cycle found for %d", mid)
				}
			}

			err := o.walk(mid, append(path, mid))
			if err != nil {
				return err
			}
		}
	}

	if o.ctx.Err() != nil {
		return o.ctx.Err()
	}

	o.visited[id] = struct{}{}
	select {
	case o.out <- id:
	case <-o.ctx.Done():
		return o.ctx.Err()
	}

	return nil
}

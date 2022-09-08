package core

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/andybar2/graphjin/core/internal/qcode"
	"github.com/andybar2/graphjin/internal/jsn"
)

func (c *gcontext) execRemoteJoin(ctx context.Context, res queryResp) (queryResp, error) {
	var err error
	sel := res.qc.st.qc.Selects

	// fetch the field name used within the db response json
	// that are used to mark insertion points and the mapping between
	// those field names and their select objects
	fids, sfmap, err := c.parentFieldIds(sel, res.qc.st.qc.Remotes)
	if err != nil {
		return res, err
	}

	// fetch the field values of the marked insertion points
	// these values contain the id to be used with fetching remote data
	from := jsn.Get(res.data, fids)
	var to []jsn.Field

	if len(from) == 0 {
		return res, errors.New("something wrong no remote ids found in db response")
	}

	to, err = c.resolveRemotes(ctx, from, sel, sfmap)
	if err != nil {
		return res, err
	}

	var ob bytes.Buffer

	err = jsn.Replace(&ob, res.data, from, to)
	if err != nil {
		return res, err
	}
	res.data = ob.Bytes()

	return res, nil
}

func (c *gcontext) resolveRemotes(
	ctx context.Context,
	from []jsn.Field,
	sel []qcode.Select,
	sfmap map[string]*qcode.Select) ([]jsn.Field, error) {

	// replacement data for the marked insertion points
	// key and value will be replaced by whats below
	to := make([]jsn.Field, len(from))

	var wg sync.WaitGroup
	wg.Add(len(from))

	var cerr error

	for i, id := range from {
		// use the json key to find the related Select object
		s, ok := sfmap[string(id.Key)]
		if !ok {
			return nil, fmt.Errorf("invalid remote field key")
		}
		p := sel[s.ParentID]

		// then use the Table name in the Select and it's parent
		// to find the resolver to use for this relationship
		r, ok := c.gj.rmap[(s.Table + p.Table)]
		if !ok {
			return nil, fmt.Errorf("no resolver found")
		}

		id := jsn.Value(id.Value)
		if len(id) == 0 {
			return nil, fmt.Errorf("invalid remote field id")
		}

		go func(n int, id []byte, s *qcode.Select) {
			defer wg.Done()

			//st := time.Now()

			ctx1, span := c.gj.spanStart(ctx, "Execute Remote Request")

			b, err := r.Fn.Resolve(ctx1, ResolverReq{
				ID: string(id), Sel: s, Log: c.gj.log, ReqConfig: c.rc})

			if err != nil {
				cerr = fmt.Errorf("%s: %s", s.Table, err)
				spanError(span, cerr)
			}
			span.End()

			if err != nil {
				return
			}

			if len(r.Path) != 0 {
				b = jsn.Strip(b, r.Path)
			}

			var ob bytes.Buffer

			if len(s.Cols) != 0 {
				err = jsn.Filter(&ob, b, colsToList(s.Cols))
				if err != nil {
					cerr = fmt.Errorf("%s: %w", s.Table, err)
					return
				}

			} else {
				ob.WriteString("null")
			}

			to[n] = jsn.Field{Key: []byte(s.FieldName), Value: ob.Bytes()}
		}(i, id, s)
	}
	wg.Wait()

	return to, cerr
}

func (c *gcontext) parentFieldIds(sel []qcode.Select, remotes int32) (
	[][]byte, map[string]*qcode.Select, error) {

	// list of keys (and it's related value) to extract from
	// the db json response
	fm := make([][]byte, 0, remotes)

	// mapping between the above extracted key and a Select
	// object
	sm := make(map[string]*qcode.Select, remotes)

	for i := range sel {
		s := &sel[i]

		if s.SkipRender != qcode.SkipTypeRemote {
			continue
		}

		p := sel[s.ParentID]

		if r, ok := c.gj.rmap[(s.Table + p.Table)]; ok {
			fm = append(fm, r.IDField)
			sm[string(r.IDField)] = s
		}
	}
	return fm, sm, nil
}

func colsToList(cols []qcode.Column) []string {
	var f []string

	for _, col := range cols {
		f = append(f, col.FieldName)
	}
	return f
}

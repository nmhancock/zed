package function

import (
	"github.com/brimdata/zed"
	"github.com/brimdata/zed/pkg/field"
)

// https://github.com/brimdata/zed/blob/main/docs/language/functions.md#nest_dotted.md
type NestDotted struct {
	zctx        *zed.Context
	builders    map[int]*zed.RecordBuilder
	recordTypes map[int]*zed.TypeRecord
}

// NewNestDotted returns a function that turns successive dotted
// field names into nested records.  For example, unflattening {"a.a":
// 1, "a.b": 1} results in {a:{a:1,b:1}}.  Note that while
// unflattening is applied recursively from the top-level and applies
// to arbitrary-depth dotted names, it is not applied to dotted names
// that start at lower levels (for example {a:{"a.a":1}} is
// unchanged).
func NewNestDotted(zctx *zed.Context) *NestDotted {
	return &NestDotted{
		zctx:        zctx,
		builders:    make(map[int]*zed.RecordBuilder),
		recordTypes: make(map[int]*zed.TypeRecord),
	}
}

func (n *NestDotted) lookupBuilderAndType(in *zed.TypeRecord) (*zed.RecordBuilder, *zed.TypeRecord, error) {
	if b, ok := n.builders[in.ID()]; ok {
		return b, n.recordTypes[in.ID()], nil
	}
	var foundDotted bool
	var fields field.List
	var types []zed.Type
	for _, c := range in.Columns {
		dotted := field.Dotted(c.Name)
		if len(dotted) > 1 {
			foundDotted = true
		}
		fields = append(fields, dotted)
		types = append(types, c.Type)
	}
	if !foundDotted {
		return nil, nil, nil
	}
	b, err := zed.NewRecordBuilder(n.zctx, fields)
	if err != nil {
		return nil, nil, err
	}
	typ := n.zctx.MustLookupTypeRecord(b.TypedColumns(types))
	n.builders[in.ID()] = b
	n.recordTypes[in.ID()] = typ
	return b, typ, nil
}

func (n *NestDotted) Call(ctx zed.Allocator, args []zed.Value) *zed.Value {
	this := &args[0]
	b, typ, err := n.lookupBuilderAndType(zed.TypeRecordOf(this.Type))
	if err != nil {
		return ctx.CopyValue(n.zctx.NewErrorf("unflatten: %s", err))
	}
	if b == nil {
		return this
	}
	b.Reset()
	for it := this.Bytes.Iter(); !it.Done(); {
		b.Append(it.Next())
	}
	zbytes, err := b.Encode()
	if err != nil {
		panic(err)
	}
	return ctx.NewValue(typ, zbytes)
}

package zed

import (
	"errors"
	"fmt"
	"strings"

	"github.com/brimdata/zed/pkg/field"
	"github.com/brimdata/zed/zcode"
)

// fieldInfo encodes the structure of a particular proc that writes a
// sequence of fields, which may potentially be inside nested records.
// This encoding enables the runtime processing to happen as efficiently
// as possible.  When handling an input record, we build an output record
// using a zcode.Builder but when handling fields within nested records,
// calls to BeginContainer() and EndContainer() on the builder need to
// happen at the right times to yield the proper output structure.
// This is probably best illustrated with an example, consider the proc
// "cut a, b.c, b.d, x.y.z".
//
// At runtime, this needs to turn into the following actions:
// 1.  builder.Append([value of a from the input record])
// 2.  builder.BeginContainer()  // for "b"
// 3.  builder.Append([value of b.c from the input record])
// 4.  builder.Append([value of b.d from the input record])
// 5.  builder.EndContainer()    // for "b"
// 6.  builder.BeginContainer()  // for "x"
// 7.  builder.BeginContainer()  // for "x.y"
// 8.  builder.Append([value of x.y.z. from the input record])
// 9.  builder.EndContainer()    // for "x.y"
// 10. builder.EndContainer()    // for "y"
//
// This is encoded into the following fieldInfo objects:
//
//	{name: "a", fullname: "a", containerBegins: [], containerEnds: 0}         // step 1
//	{name: "c", fullname: "b.c", containerBegins: ["b"], containerEnds: 0}      // steps 2-3
//	{name: "d", fullname: "b.d", containerBegins: [], containerEnds: 1     }    // steps 4-5
//	{name: "z", fullname: "x.y.z", containerBegins: ["x", "y"], containerEnds: 2} // steps 6-10
type fieldInfo struct {
	field           field.Path
	containerBegins []string
	containerEnds   int
}

type RecordBuilder struct {
	fields   []fieldInfo
	builder  *zcode.Builder
	zctx     *Context
	curField int
}

// NewRecordBuilder constructs the zcode.Bytes representation for records
// built from an array of input field selectors expressed as field.Path.
// Append should be called to enter field values in the left to right order
// of the provided fields and Encode is called to retrieve the nested zcode.Bytes
// value.  Reset should be called before encoding the next record.
func NewRecordBuilder(zctx *Context, fields field.List) (*RecordBuilder, error) {
	seenRecords := make(map[string]bool)
	fieldInfos := make([]fieldInfo, 0, len(fields))
	var currentRecord []string
	for i, field := range fields {
		if field.IsEmpty() {
			return nil, errors.New("empty field path")
		}
		names := field
		// Grab everything except the leaf field name and see if
		// it has changed from the previous field.  If it hasn't,
		// things are simple but if it has, we need to carefully
		// figure out which records we are stepping in and out of.
		record := names[:len(names)-1]
		var containerBegins []string
		if !sameRecord(record, currentRecord) {
			// currentRecord is what nested record the zcode.Builder
			// is currently working on, record is the nested
			// record for the current field.  First figure out
			// what (if any) common parents are shared.
			l := len(currentRecord)
			if len(record) < l {
				l = len(record)
			}
			pos := 0
			for pos < l {
				if record[pos] != currentRecord[pos] {
					break
				}
				pos += 1
			}

			// Note any previously encoded records that are
			// now finished.
			if i > 0 {
				fieldInfos[i-1].containerEnds = len(currentRecord) - pos
			}

			// Validate any new records that we're starting
			// (i.e., ensure that we didn't handle fields from
			// the same record previously), then record the names
			// of all these records.
			for pos2 := pos; pos2 < len(record); pos2++ {
				recname := strings.Join(record[:pos2+1], ".")
				_, seen := seenRecords[recname]
				if seen {
					return nil, fmt.Errorf("fields in record %s must be adjacent", recname)
				}
				seenRecords[recname] = true
				containerBegins = append(containerBegins, record[pos2])
			}
			currentRecord = record
		}
		if isIn(field, fieldInfos) {
			return nil, &DuplicateFieldError{strings.Join(field, ".")}
		}
		fieldInfos = append(fieldInfos, fieldInfo{field, containerBegins, 0})
	}
	if len(fieldInfos) > 0 {
		fieldInfos[len(fieldInfos)-1].containerEnds = len(currentRecord)
	}

	return &RecordBuilder{
		fields:  fieldInfos,
		builder: zcode.NewBuilder(),
		zctx:    zctx,
	}, nil
}

// check if fieldname is "in" one of the fields in fis, or if
// one of fis is "in" fieldname, where "in" means "equal or is a suffix of".
func isIn(fieldname field.Path, fis []fieldInfo) bool {
	// check if fieldname of splits is "in" one of fis
	for i := range fieldname {
		sub := fieldname[:i+1]
		for _, fi := range fis {
			if sub.Equal(fi.field) {
				return true
			}
		}
	}
	// check if one of fis is "in" fieldname
	for _, fi := range fis {
		for i := range fi.field {
			prefix := fi.field[:i+1]
			if prefix.Equal(fieldname) {
				return true
			}
		}
	}
	return false
}

func sameRecord(names1, names2 []string) bool {
	if len(names1) != len(names2) {
		return false
	}
	for i := range names1 {
		if names1[i] != names2[i] {
			return false
		}
	}
	return true
}

func (r *RecordBuilder) Reset() {
	r.builder.Reset()
	r.curField = 0
}

func (r *RecordBuilder) Append(leaf []byte) {
	field := r.fields[r.curField]
	r.curField++
	for range field.containerBegins {
		r.builder.BeginContainer()
	}
	r.builder.Append(leaf)
	for i := 0; i < field.containerEnds; i++ {
		r.builder.EndContainer()
	}
}

func (r *RecordBuilder) Encode() (zcode.Bytes, error) {
	if r.curField != len(r.fields) {
		return nil, errors.New("did not receive enough columns")
	}
	return r.builder.Bytes(), nil
}

// A RecordBuilder understands the shape of a sequence of FieldExprs
// (i.e., which columns are inside nested records) but not the types.
// TypedColumns takes an array of Types for the individual fields
// and constructs an array of Columns that reflects the fullly
// typed structure.  This is suitable for e.g. allocating a descriptor.
func (r *RecordBuilder) TypedColumns(types []Type) []Column {
	type rec struct {
		name string
		cols []Column
	}
	current := &rec{"", nil}
	stack := make([]*rec, 1)
	stack[0] = current

	for i, fi := range r.fields {
		for _, name := range fi.containerBegins {
			current = &rec{name, nil}
			stack = append(stack, current)
		}

		current.cols = append(current.cols, NewColumn(fi.field.Leaf(), types[i]))

		for j := 0; j < fi.containerEnds; j++ {
			recType := r.zctx.MustLookupTypeRecord(current.cols)
			slen := len(stack)
			stack = stack[:slen-1]
			cur := stack[slen-2]
			cur.cols = append(cur.cols, NewColumn(current.name, recType))
			current = cur
		}
	}
	if len(stack) != 1 {
		panic("Mismatched container begin/end")
	}
	return stack[0].cols
}

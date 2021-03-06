package database_test

import (
	"encoding/binary"
	"errors"
	"fmt"
	"testing"

	"github.com/genjidb/genji/database"
	"github.com/genjidb/genji/document"
	"github.com/genjidb/genji/document/encoding/msgpack"
	"github.com/genjidb/genji/engine/memoryengine"
	"github.com/genjidb/genji/key"
	"github.com/genjidb/genji/sql/parser"
	"github.com/stretchr/testify/require"
)

func parsePath(t testing.TB, str string) document.ValuePath {
	vp, err := parser.ParsePath(str)
	require.NoError(t, err)
	return vp
}

func newTestTable(t testing.TB) (*database.Table, func()) {
	tx, fn := newTestDB(t)

	err := tx.CreateTable("test", nil)
	require.NoError(t, err)
	tb, err := tx.GetTable("test")
	require.NoError(t, err)

	return tb, fn
}

func newDocument() *document.FieldBuffer {
	return document.NewFieldBuffer().
		Add("fielda", document.NewTextValue("a")).
		Add("fieldb", document.NewTextValue("b"))
}

// TestTableIterate verifies Iterate behaviour.
func TestTableIterate(t *testing.T) {
	t.Run("Should not fail with no documents", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		i := 0
		err := tb.Iterate(func(d document.Document) error {
			i++
			return nil
		})
		require.NoError(t, err)
		require.Zero(t, i)
	})

	t.Run("Should iterate over all documents", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		for i := 0; i < 10; i++ {
			_, err := tb.Insert(newDocument())
			require.NoError(t, err)
		}

		m := make(map[string]int)
		err := tb.Iterate(func(d document.Document) error {
			m[string(d.(document.Keyer).Key())]++
			return nil
		})
		require.NoError(t, err)
		require.Len(t, m, 10)
		for _, c := range m {
			require.Equal(t, 1, c)
		}
	})

	t.Run("Should stop if fn returns error", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		for i := 0; i < 10; i++ {
			_, err := tb.Insert(newDocument())
			require.NoError(t, err)
		}

		i := 0
		err := tb.Iterate(func(_ document.Document) error {
			i++
			if i >= 5 {
				return errors.New("some error")
			}
			return nil
		})
		require.EqualError(t, err, "some error")
		require.Equal(t, 5, i)
	})
}

// TestTableGetDocument verifies GetDocument behaviour.
func TestTableGetDocument(t *testing.T) {
	t.Run("Should fail if not found", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		r, err := tb.GetDocument([]byte("id"))
		require.Equal(t, database.ErrDocumentNotFound, err)
		require.Nil(t, r)
	})

	t.Run("Should return the right document", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		// create two documents, one with an additional field
		doc1 := newDocument()
		vc := document.NewIntegerValue(40)
		doc1.Add("fieldc", vc)
		doc2 := newDocument()

		key1, err := tb.Insert(doc1)
		require.NoError(t, err)
		_, err = tb.Insert(doc2)
		require.NoError(t, err)

		// fetch doc1 and make sure it returns the right one
		res, err := tb.GetDocument(key1)
		require.NoError(t, err)
		fc, err := res.GetByField("fieldc")
		require.NoError(t, err)
		require.Equal(t, vc, fc)
	})
}

// TestTableInsert verifies Insert behaviour.
func TestTableInsert(t *testing.T) {
	t.Run("Should generate a key by default", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		doc := newDocument()
		key1, err := tb.Insert(doc)
		require.NoError(t, err)
		require.NotEmpty(t, key1)

		key2, err := tb.Insert(doc)
		require.NoError(t, err)
		require.NotEmpty(t, key2)

		require.NotEqual(t, key1, key2)
	})

	t.Run("Should generate the right docid on existing databases", func(t *testing.T) {
		ng := memoryengine.NewEngine()

		db, err := database.New(ng, database.Options{Codec: msgpack.NewCodec()})
		require.NoError(t, err)

		insertDoc := func(db *database.Database) []byte {
			tx, err := db.Begin(true)
			require.NoError(t, err)

			_ = tx.CreateTable("test", &database.TableInfo{})

			tb, err := tx.GetTable("test")
			require.NoError(t, err)

			doc := newDocument()
			key, err := tb.Insert(doc)
			require.NoError(t, err)
			require.NotEmpty(t, key)

			err = tx.Commit()
			require.NoError(t, err)

			return key
		}

		key1 := insertDoc(db)
		require.NoError(t, err)

		// create new database object
		db, err = database.New(ng, database.Options{Codec: msgpack.NewCodec()})
		require.NoError(t, err)

		key2 := insertDoc(db)

		a, _ := binary.Uvarint(key1)
		require.NoError(t, err)

		b, _ := binary.Uvarint(key2)
		require.NoError(t, err)

		require.Equal(t, a+1, b)
	})

	t.Run("Should use the right field if primary key is specified", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{Path: parsePath(t, "foo.a[1]"), Type: document.IntegerValue, IsPrimaryKey: true},
			},
		})
		require.NoError(t, err)
		tb, err := tx.GetTable("test")
		require.NoError(t, err)

		var doc document.FieldBuffer
		err = doc.UnmarshalJSON([]byte(`{"foo": {"a": [0, 10]}}`))
		require.NoError(t, err)

		// insert
		k, err := tb.Insert(doc)
		require.NoError(t, err)
		require.Equal(t, key.AppendInt64(nil, 10), k)

		// make sure the document is fetchable using the returned key
		_, err = tb.GetDocument(k)
		require.NoError(t, err)

		// insert again
		k, err = tb.Insert(doc)
		require.Equal(t, database.ErrDuplicateDocument, err)
	})

	t.Run("Should convert values into the right types if there are constraints", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{Path: parsePath(t, "foo"), Type: document.ArrayValue},
				{Path: parsePath(t, "foo[0]"), Type: document.IntegerValue},
			},
		})
		require.NoError(t, err)
		tb, err := tx.GetTable("test")
		require.NoError(t, err)

		var doc document.FieldBuffer
		err = doc.UnmarshalJSON([]byte(`{"foo": [100]}`))
		require.NoError(t, err)

		// insert
		key, err := tb.Insert(doc)
		require.NoError(t, err)

		d, err := tb.GetDocument(key)
		require.NoError(t, err)

		v, err := parsePath(t, "foo[0]").GetValue(d)
		require.NoError(t, err)
		require.Equal(t, document.NewIntegerValue(100), v)
	})

	t.Run("Should fail if Pk not found in document or empty", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{Path: parsePath(t, "foo"), Type: document.IntegerValue, IsPrimaryKey: true},
			},
		})
		require.NoError(t, err)
		tb, err := tx.GetTable("test")
		require.NoError(t, err)

		tests := [][]byte{
			nil,
			{},
			[]byte(nil),
		}

		for _, test := range tests {
			t.Run(fmt.Sprintf("%#v", test), func(t *testing.T) {
				doc := document.NewFieldBuffer().
					Add("foo", document.NewBlobValue(test))

				_, err := tb.Insert(doc)
				require.Error(t, err)
			})
		}
	})

	t.Run("Should update indexes if there are indexed fields", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test", nil)
		require.NoError(t, err)

		err = tx.CreateIndex(database.IndexConfig{
			IndexName: "idxFoo", TableName: "test", Path: parsePath(t, "foo"),
		})
		require.NoError(t, err)
		idx, err := tx.GetIndex("idxFoo")
		require.NoError(t, err)

		tb, err := tx.GetTable("test")
		require.NoError(t, err)

		// create one document with the foo field
		doc1 := newDocument()
		foo := document.NewDoubleValue(10)
		doc1.Add("foo", foo)

		// create one document without the foo field
		doc2 := newDocument()

		key1, err := tb.Insert(doc1)
		require.NoError(t, err)
		key2, err := tb.Insert(doc2)
		require.NoError(t, err)

		var count int
		err = idx.AscendGreaterOrEqual(document.Value{}, func(val, k []byte, isEqual bool) error {
			switch count {
			case 0:
				// key2, which doesn't countain the field must appear first in the next,
				// as null values are the smallest possible values
				require.Equal(t, key2, k)
			case 1:
				require.Equal(t, key1, k)
			}
			count++
			return nil
		})
		require.NoError(t, err)
		require.Equal(t, 2, count)
	})

	t.Run("Should convert the fields if FieldsConstraints are specified", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{parsePath(t, "foo"), document.IntegerValue, false, false},
				{parsePath(t, "bar"), document.IntegerValue, false, false},
			},
		})
		require.NoError(t, err)
		tb, err := tx.GetTable("test")
		require.NoError(t, err)

		doc := document.NewFieldBuffer().
			Add("foo", document.NewIntegerValue(1)).
			Add("bar", document.NewDoubleValue(10)).
			Add("baz", document.NewTextValue("baaaaz"))

		// insert
		key, err := tb.Insert(doc)
		require.NoError(t, err)

		// make sure the fields have been converted to the right types
		d, err := tb.GetDocument(key)
		require.NoError(t, err)
		v, err := d.GetByField("foo")
		require.NoError(t, err)
		require.Equal(t, document.NewIntegerValue(1), v)
		v, err = d.GetByField("bar")
		require.NoError(t, err)
		require.Equal(t, document.NewIntegerValue(10), v)
		v, err = d.GetByField("baz")
		require.NoError(t, err)
		require.Equal(t, document.NewTextValue("baaaaz"), v)
	})

	t.Run("Should fail if there is a not null field constraint on a document field and the field is null or missing", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		// no enforced type, not null
		err := tx.CreateTable("test1", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{parsePath(t, "foo"), 0, false, true},
			},
		})
		require.NoError(t, err)
		tb1, err := tx.GetTable("test1")
		require.NoError(t, err)

		// enforced type, not null
		err = tx.CreateTable("test2", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{parsePath(t, "foo"), document.IntegerValue, false, true},
			},
		})
		require.NoError(t, err)
		tb2, err := tx.GetTable("test2")
		require.NoError(t, err)

		// insert with empty foo field should fail
		_, err = tb1.Insert(document.NewFieldBuffer().
			Add("bar", document.NewDoubleValue(1)))
		require.Error(t, err)

		// insert with null foo field should fail
		_, err = tb1.Insert(document.NewFieldBuffer().
			Add("foo", document.NewNullValue()))
		require.Error(t, err)

		// otherwise it should work
		_, err = tb1.Insert(document.NewFieldBuffer().
			Add("foo", document.NewDoubleValue(1)))
		require.NoError(t, err)

		// insert with empty foo field should fail
		_, err = tb2.Insert(document.NewFieldBuffer().
			Add("bar", document.NewDoubleValue(1)))
		require.Error(t, err)

		// insert with null foo field should fail
		_, err = tb2.Insert(document.NewFieldBuffer().
			Add("foo", document.NewNullValue()))
		require.Error(t, err)

		// otherwise it should work
		_, err = tb2.Insert(document.NewFieldBuffer().
			Add("foo", document.NewDoubleValue(1)))
		require.NoError(t, err)
	})

	t.Run("Should fail if there is a not null field constraint on an array value and the value is null", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test1", &database.TableInfo{
			FieldConstraints: []database.FieldConstraint{
				{parsePath(t, "foo[1]"), 0, false, true},
			},
		})
		require.NoError(t, err)
		tb, err := tx.GetTable("test1")
		require.NoError(t, err)

		// insert table with only one value
		_, err = tb.Insert(document.NewFieldBuffer().
			Add("foo", document.NewArrayValue(document.NewValueBuffer().Append(document.NewIntegerValue(1)))))
		require.Error(t, err)
		_, err = tb.Insert(document.NewFieldBuffer().
			Add("foo", document.NewArrayValue(document.NewValueBuffer().
				Append(document.NewIntegerValue(1)).Append(document.NewIntegerValue(2)))))
		require.NoError(t, err)
	})
}

// TestTableDelete verifies Delete behaviour.
func TestTableDelete(t *testing.T) {
	t.Run("Should fail if not found", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		err := tb.Delete([]byte("id"))
		require.Equal(t, database.ErrDocumentNotFound, err)
	})

	t.Run("Should delete the right document", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		// create two documents, one with an additional field
		doc1 := newDocument()
		doc1.Add("fieldc", document.NewIntegerValue(40))
		doc2 := newDocument()

		key1, err := tb.Insert(doc1)
		require.NoError(t, err)
		key2, err := tb.Insert(doc2)
		require.NoError(t, err)

		// delete the document
		err = tb.Delete([]byte(key1))
		require.NoError(t, err)

		// try again, should fail
		err = tb.Delete([]byte(key1))
		require.Equal(t, database.ErrDocumentNotFound, err)

		// make sure it didn't also delete the other one
		res, err := tb.GetDocument(key2)
		require.NoError(t, err)
		_, err = res.GetByField("fieldc")
		require.Error(t, err)
	})
}

// TestTableReplace verifies Replace behaviour.
func TestTableReplace(t *testing.T) {
	t.Run("Should fail if not found", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		err := tb.Replace([]byte("id"), newDocument())
		require.Equal(t, database.ErrDocumentNotFound, err)
	})

	t.Run("Should replace the right document", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		// create two different documents
		doc1 := newDocument()
		doc2 := document.NewFieldBuffer().
			Add("fielda", document.NewTextValue("c")).
			Add("fieldb", document.NewTextValue("d"))

		key1, err := tb.Insert(doc1)
		require.NoError(t, err)
		key2, err := tb.Insert(doc2)
		require.NoError(t, err)

		// create a third document
		doc3 := document.NewFieldBuffer().
			Add("fielda", document.NewTextValue("e")).
			Add("fieldb", document.NewTextValue("f"))

		// replace doc1 with doc3
		err = tb.Replace(key1, doc3)
		require.NoError(t, err)

		// make sure it replaced it cordoctly
		res, err := tb.GetDocument(key1)
		require.NoError(t, err)
		f, err := res.GetByField("fielda")
		require.NoError(t, err)
		require.Equal(t, "e", f.V.(string))

		// make sure it didn't also replace the other one
		res, err = tb.GetDocument(key2)
		require.NoError(t, err)
		f, err = res.GetByField("fielda")
		require.NoError(t, err)
		require.Equal(t, "c", f.V.(string))
	})
}

// TestTableTruncate verifies Truncate behaviour.
func TestTableTruncate(t *testing.T) {
	t.Run("Should succeed if table empty", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		err := tb.Truncate()
		require.NoError(t, err)
	})

	t.Run("Should truncate the table", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		// create two documents
		doc1 := newDocument()
		doc2 := newDocument()

		_, err := tb.Insert(doc1)
		require.NoError(t, err)
		_, err = tb.Insert(doc2)
		require.NoError(t, err)

		err = tb.Truncate()
		require.NoError(t, err)

		err = tb.Iterate(func(_ document.Document) error {
			return errors.New("should not iterate")
		})

		require.NoError(t, err)
	})
}

func TestTableReIndex(t *testing.T) {
	t.Run("Should succeed if table has no index", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		err := tb.ReIndex()
		require.NoError(t, err)
	})

	t.Run("Should reindex the right indexes", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test1", nil)
		require.NoError(t, err)
		err = tx.CreateTable("test2", nil)
		require.NoError(t, err)
		tb1, err := tx.GetTable("test1")
		require.NoError(t, err)
		tb2, err := tx.GetTable("test2")
		require.NoError(t, err)

		for i := int64(0); i < 10; i++ {
			doc := document.NewFieldBuffer().
				Add("a", document.NewIntegerValue(i)).
				Add("b", document.NewIntegerValue(i*10))
			_, err = tb1.Insert(doc)
			require.NoError(t, err)
			_, err = tb2.Insert(doc)
			require.NoError(t, err)
		}

		err = tx.CreateIndex(database.IndexConfig{
			IndexName: "test1a",
			TableName: "test1",
			Path:      parsePath(t, "a"),
		})
		require.NoError(t, err)
		err = tx.CreateIndex(database.IndexConfig{
			IndexName: "test1b",
			TableName: "test1",
			Path:      parsePath(t, "b"),
		})
		require.NoError(t, err)
		err = tx.CreateIndex(database.IndexConfig{
			IndexName: "test2a",
			TableName: "test2",
			Path:      parsePath(t, "a"),
		})
		require.NoError(t, err)
		err = tx.CreateIndex(database.IndexConfig{
			IndexName: "test2b",
			TableName: "test2",
			Path:      parsePath(t, "b"),
		})
		require.NoError(t, err)

		err = tb1.ReIndex()
		require.NoError(t, err)

		countIndexElems := func(idx *database.Index) int {
			var i int
			err = idx.AscendGreaterOrEqual(document.Value{Type: document.IntegerValue}, func(v, k []byte, isEqual bool) error {
				i++
				return nil
			})
			require.NoError(t, err)
			return i
		}

		idx, err := tx.GetIndex("test1a")
		require.NoError(t, err)
		require.Equal(t, 10, countIndexElems(idx))

		idx, err = tx.GetIndex("test1b")
		require.NoError(t, err)
		require.Equal(t, 10, countIndexElems(idx))

		idx, err = tx.GetIndex("test2a")
		require.NoError(t, err)
		require.Equal(t, 0, countIndexElems(idx))

		idx, err = tx.GetIndex("test2b")
		require.NoError(t, err)
		require.Equal(t, 0, countIndexElems(idx))
	})
}

func TestTableIndexes(t *testing.T) {
	t.Run("Should succeed if table has no indexes", func(t *testing.T) {
		tb, cleanup := newTestTable(t)
		defer cleanup()

		m, err := tb.Indexes()
		require.NoError(t, err)
		require.Empty(t, m)
	})

	t.Run("Should return a map of all the indexes", func(t *testing.T) {
		tx, cleanup := newTestDB(t)
		defer cleanup()

		err := tx.CreateTable("test1", nil)
		require.NoError(t, err)
		tb, err := tx.GetTable("test1")
		require.NoError(t, err)

		err = tx.CreateTable("test2", nil)
		require.NoError(t, err)

		err = tx.CreateIndex(database.IndexConfig{
			Unique:    true,
			IndexName: "idx1a",
			TableName: "test1",
			Path:      parsePath(t, "a"),
		})
		require.NoError(t, err)
		err = tx.CreateIndex(database.IndexConfig{
			Unique:    false,
			IndexName: "idx1b",
			TableName: "test1",
			Path:      parsePath(t, "b"),
		})
		require.NoError(t, err)
		err = tx.CreateIndex(database.IndexConfig{
			Unique:    false,
			IndexName: "ifx2a",
			TableName: "test2",
			Path:      parsePath(t, "a"),
		})
		require.NoError(t, err)

		m, err := tb.Indexes()
		require.NoError(t, err)
		require.Len(t, m, 2)
		idx1a, ok := m["a"]
		require.True(t, ok)
		require.NotNil(t, idx1a)
		idx1b, ok := m["b"]
		require.True(t, ok)
		require.NotNil(t, idx1b)
	})
}

// BenchmarkTableInsert benchmarks the Insert method with 1, 10, 1000 and 10000 successive insertions.
func BenchmarkTableInsert(b *testing.B) {
	for size := 1; size <= 10000; size *= 10 {
		b.Run(fmt.Sprintf("%.05d", size), func(b *testing.B) {
			var fb document.FieldBuffer

			for i := int64(0); i < 10; i++ {
				fb.Add(fmt.Sprintf("name-%d", i), document.NewIntegerValue(i))
			}

			b.ResetTimer()
			b.StopTimer()
			for i := 0; i < b.N; i++ {
				tb, cleanup := newTestTable(b)

				b.StartTimer()
				for j := 0; j < size; j++ {
					tb.Insert(&fb)
				}
				b.StopTimer()
				cleanup()
			}
		})
	}
}

// BenchmarkTableScan benchmarks the Scan method with 1, 10, 1000 and 10000 successive insertions.
func BenchmarkTableScan(b *testing.B) {
	for size := 1; size <= 10000; size *= 10 {
		b.Run(fmt.Sprintf("%.05d", size), func(b *testing.B) {
			tb, cleanup := newTestTable(b)
			defer cleanup()

			var fb document.FieldBuffer

			for i := int64(0); i < 10; i++ {
				fb.Add(fmt.Sprintf("name-%d", i), document.NewIntegerValue(i))
			}

			for i := 0; i < size; i++ {
				_, err := tb.Insert(&fb)
				require.NoError(b, err)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				tb.Iterate(func(document.Document) error {
					return nil
				})
			}
			b.StopTimer()
		})
	}
}

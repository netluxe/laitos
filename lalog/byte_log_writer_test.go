package lalog

import (
	"bytes"
	"reflect"
	"testing"
)

func TestByteLogWriterLargeChunks(t *testing.T) {
	null := new(bytes.Buffer)
	writer := NewByteLogWriter(null, 5)
	// Overwriting entire internal buffer several times (789, 01234, 56789)
	if _, err := writer.Write([]byte{7, 8, 9, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{5, 6, 7, 8, 9}) {
		t.Fatal(writer.Retrieve(false))
	}
	// Small write again
	if _, err := writer.Write([]byte{0, 1, 2}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{8, 9, 0, 1, 2}) {
		t.Fatal(writer.Retrieve(false))
	}
	// Exactly full again
	if _, err := writer.Write([]byte{3, 4}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{0, 1, 2, 3, 4}) {
		t.Fatal(writer.Retrieve(false))
	}
	// Write couple of valid ASCII characters and retrieve (63 is question mark - the placeholder for non-ascii chars)
	if _, err := writer.Write([]byte{65, 97}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(true), []byte{63, 63, 63, 65, 97}) {
		t.Fatal(writer.Retrieve(true))
	}
}

func TestByteLogWriterSmallChunks(t *testing.T) {
	null := new(bytes.Buffer)
	writer := NewByteLogWriter(null, 5)
	// Plenty of room
	if _, err := writer.Write([]byte{0, 1}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{0, 1}) {
		t.Fatal(writer.Retrieve(false))
	}
	// Nearly full
	if _, err := writer.Write([]byte{2, 3}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{0, 1, 2, 3}) {
		t.Fatal(writer.Retrieve(false))
	}
	// Full
	if _, err := writer.Write([]byte{4}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{0, 1, 2, 3, 4}) {
		t.Fatal(writer.Retrieve(false))
	}
	// Evict the oldest byte
	if _, err := writer.Write([]byte{5}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(writer.Retrieve(false), []byte{1, 2, 3, 4, 5}) {
		t.Fatal(writer.Retrieve(false))
	}
}

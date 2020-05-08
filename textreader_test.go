// Copyright 2010 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package smtp

import (
	"bufio"
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_dotReader_Read(t *testing.T) {
	str := []byte("Test\r\nNeueZeile\r\n.\r\n")
	r := NewReader(bufio.NewReader(bytes.NewReader(str)))
	d := &dotReader{
		r: r,
	}
	buf := make([]byte, len(str)-3)
	_, err := d.Read(buf)
	if err != nil && err != io.EOF {
		assert.NoError(t, err)
	}

	assert.Equal(t, str[0:len(str)-3], buf)
}

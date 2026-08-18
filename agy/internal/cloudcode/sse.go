package cloudcode

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

type SSEEvent struct {
	Data []byte
}

// SSEDecoder keeps event data as bytes until JSON decoding. This matters for
// UTF-8 text split across network reads: converting each read to a string
// would risk inserting replacement characters before the event is complete.
type SSEDecoder struct {
	reader   *bufio.Reader
	data     bytes.Buffer
	eof      bool
	dispatch bool
}

func NewSSEDecoder(reader io.Reader) *SSEDecoder {
	return &SSEDecoder{reader: bufio.NewReaderSize(reader, 64*1024)}
}

func (d *SSEDecoder) Next() (SSEEvent, error) {
	if d == nil || d.reader == nil {
		return SSEEvent{}, errors.New("SSE decoder is not initialized")
	}
	for {
		line, err := d.reader.ReadBytes('\n')
		if len(line) > 0 {
			line = bytes.TrimSuffix(line, []byte{'\n'})
			line = bytes.TrimSuffix(line, []byte{'\r'})
			if len(line) == 0 {
				if d.data.Len() > 0 {
					return d.takeEvent(), nil
				}
			} else if line[0] == ':' {
				// SSE comment/keep-alive.
			} else if bytes.HasPrefix(line, []byte("data:")) {
				value := line[len("data:"):]
				if len(value) > 0 && value[0] == ' ' {
					value = value[1:]
				}
				d.data.Write(value)
				d.data.WriteByte('\n')
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if d.data.Len() > 0 {
					return d.takeEvent(), nil
				}
				return SSEEvent{}, io.EOF
			}
			return SSEEvent{}, err
		}
	}
}

func (d *SSEDecoder) takeEvent() SSEEvent {
	data := bytes.TrimSuffix(d.data.Bytes(), []byte{'\n'})
	result := SSEEvent{Data: append([]byte(nil), data...)}
	d.data.Reset()
	return result
}

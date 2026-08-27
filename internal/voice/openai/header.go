package openai

import "net/textproto"

func textprotoHeader(value map[string][]string) textproto.MIMEHeader {
	return textproto.MIMEHeader(value)
}

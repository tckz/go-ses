package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/mail"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"github.com/samber/lo"
	"gopkg.in/gomail.v2"
)

type stringsFlag []string

func (f *stringsFlag) String() string {
	if f == nil {
		return "[]"
	}
	return fmt.Sprintf("%v", *f)
}

func (f *stringsFlag) Set(v string) error {
	*f = append(*f, v)
	return nil
}

func main() {
	godotenv.Load()

	var optTo stringsFlag
	var optCC stringsFlag
	var optBCC stringsFlag
	flag.Var(&optTo, "to", "To address")
	flag.Var(&optCC, "cc", "Cc address")
	flag.Var(&optBCC, "bcc", "Bcc address")
	optBody := flag.String("body", "", "/path/to/mail-body.txt")
	optFrom := flag.String("from", "", "From address")
	optCharset := flag.String("charset", "", "Charset")
	optEncoding := flag.String("encoding", "", "MIME Encoding(quoted-printable|base64|8bit)")
	optSubject := flag.String("subject", "", "Subject")
	optContentType := flag.String("content-type", "text/plain", "Content-Type")
	optConfigurationSet := flag.String("configuration-set", "", "SES configuration set")
	optNoSend := flag.Bool("no-send", false, "Don't send email, just print raw message")
	flag.Parse()

	if *optBody == "" {
		log.Fatalf("*** --body must be specified")
	}

	if *optFrom == "" {
		log.Fatalf("*** --from must be specified")
	}

	b, err := os.ReadFile(*optBody)
	if err != nil {
		log.Fatalf("*** ReadFile: %v", err)
	}

	var opts []gomail.MessageSetting
	if *optCharset != "" {
		opts = append(opts, gomail.SetCharset(*optCharset))
	}
	if *optEncoding != "" {
		opts = append(opts, gomail.SetEncoding(gomail.Encoding(*optEncoding)))
	}
	m := gomail.NewMessage(opts...)
	fromDomain := ""
	var fromAddr *mail.Address
	{
		addr, err := mail.ParseAddress(*optFrom)
		if err != nil {
			log.Fatalf("*** ParseAddress(%s): %v", *optFrom, err)
		}
		m.SetAddressHeader("From", addr.Address, addr.Name)
		fromAddr = addr

		// ParseAddressを通過しているので、ドメイン名部分が存在する
		i := strings.LastIndexByte(addr.Address, '@')
		fromDomain = addr.Address[i+1:]
	}
	if *optSubject != "" {
		m.SetHeader("Subject", *optSubject)
	}

	var tos []*mail.Address
	var ccs []*mail.Address
	var bccs []*mail.Address
	appendDest := func(addrs []string, dests *[]*mail.Address) error {
		for _, to := range addrs {
			addr, err := mail.ParseAddress(to)
			if err != nil {
				return fmt.Errorf("mail.ParseAddress(%s): %v", to, err)
			}
			*dests = append(*dests, addr)
		}
		return nil
	}
	appendDestAndHeader := func(addrs []string, field string, dests *[]*mail.Address) error {
		err := appendDest(addrs, dests)
		if err != nil {
			return err
		}
		if len(addrs) > 0 {
			addrs := lo.Map(*dests, func(addr *mail.Address, _ int) string { return m.FormatAddress(addr.Address, addr.Name) })
			m.SetHeader(field, addrs...)
		}
		return nil
	}
	if err := appendDestAndHeader(optTo, "To", &tos); err != nil {
		log.Fatalf("*** To: %v", err)
	}
	if err := appendDestAndHeader(optCC, "Cc", &ccs); err != nil {
		log.Fatalf("*** Cc: %v", err)
	}
	if err := appendDest(optBCC, &bccs); err != nil {
		log.Fatalf("*** Bcc: %v", err)
	}

	msgid := uuid.New().String() + "@" + fromDomain
	// SESによって上書きされ、この値は使われない
	m.SetHeader("Message-ID", "<"+msgid+">")

	m.SetBody(*optContentType, string(b))

	sb := &bytes.Buffer{}
	m.WriteTo(sb)
	os.Stderr.Write(sb.Bytes())
	os.Stderr.WriteString("\n")

	if *optNoSend {
		return
	}

	ctx := context.Background()
	cfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(os.Getenv("AWS_REGION")))
	if err != nil {
		log.Fatalf("config.LoadDefaultConfig: %v", err)
	}

	cl := sesv2.NewFromConfig(cfg)
	in := &sesv2.SendEmailInput{
		Content: &types.EmailContent{
			Raw: &types.RawMessage{Data: sb.Bytes()},
		},
		Destination: &types.Destination{
			BccAddresses: lo.Map(bccs, func(addr *mail.Address, _ int) string { return addr.Address }),
			CcAddresses:  lo.Map(ccs, func(addr *mail.Address, _ int) string { return addr.Address }),
			ToAddresses:  lo.Map(tos, func(addr *mail.Address, _ int) string { return addr.Address }),
		},
		FromEmailAddress: aws.String(fromAddr.Address),
	}

	if *optConfigurationSet != "" {
		in.ConfigurationSetName = optConfigurationSet
	}

	out, err := cl.SendEmail(ctx, in)
	if err != nil {
		log.Fatalf("*** SendEmail: %v", err)
	}

	{
		b, err := marshalYaml(out)
		if err != nil {
			log.Fatalf("*** marshalYaml: %v", err)
		}

		fmt.Fprintf(os.Stderr, "---\n%s\n", string(b))
	}
}

// marshalYaml yaml tagのないstructをyaml.Marshalするとkeyがlowercaseになってしまうので一回JSONでmap[string]interface{}に変換してからyaml.Marshalすると大文字小文字を維持できる
func marshalYaml(v any) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("json.Marshal: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var o any
	err = dec.Decode(&o)
	if err != nil {
		return nil, fmt.Errorf("dec.Decode: %w", err)
	}
	b, err = yaml.MarshalWithOptions(o, yaml.UseLiteralStyleIfMultiline(true))
	if err != nil {
		return nil, fmt.Errorf("yaml.MarshalWithOptions: %w", err)
	}
	return b, nil
}

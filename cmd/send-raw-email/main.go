package main

import (
	"bytes"
	"flag"
	"fmt"
	"log"
	"net/mail"
	"os"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ses"
	"github.com/google/uuid"
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
	var optTo stringsFlag
	var optCC stringsFlag
	var optBCC stringsFlag
	flag.Var(&optTo, "to", "To address")
	flag.Var(&optCC, "cc", "Cc address")
	flag.Var(&optBCC, "bcc", "Bcc address")
	optRegion := flag.String("region", "ap-northeast-1", "AWS region")
	optBody := flag.String("body", "", "/path/to/mail-body.txt")
	optFrom := flag.String("from", "", "From address")
	optSubject := flag.String("subject", "", "Subject")
	optConfigurationSet := flag.String("configuration-set", "", "SES configuration set")
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

	m := gomail.NewMessage()
	fromDomain := ""
	{
		addr, err := mail.ParseAddress(*optFrom)
		if err != nil {
			log.Fatalf("*** ParseAddress(%s): %v", *optFrom, err)
		}
		m.SetHeader("From", *optFrom)

		// ParseAddressを通過しているので、ドメイン名部分が存在する
		i := strings.LastIndexByte(addr.Address, '@')
		fromDomain = addr.Address[i+1:]
	}
	if *optSubject != "" {
		m.SetHeader("Subject", *optSubject)
	}

	var dests []*string
	appendDest := func(addrs []string) error {
		for _, to := range optTo {
			addr, err := mail.ParseAddress(to)
			if err != nil {
				return fmt.Errorf("mail.ParseAddress(%s): %v", to, err)
			}
			dests = append(dests, aws.String(addr.Address))
		}
		return nil
	}
	appendDestAndHeader := func(addrs []string, field string) error {
		err := appendDest(addrs)
		if err != nil {
			return err
		}
		if len(addrs) > 0 {
			m.SetHeader(field, addrs...)
		}
		return nil
	}
	if err := appendDestAndHeader(optTo, "To"); err != nil {
		log.Fatalf("*** To: %v", err)
	}
	if err := appendDestAndHeader(optCC, "Cc"); err != nil {
		log.Fatalf("*** Cc: %v", err)
	}
	if err := appendDest(optBCC); err != nil {
		log.Fatalf("*** Bcc: %v", err)
	}

	msgid := uuid.New().String() + "@" + fromDomain
	m.SetHeader("Message-ID", "<"+msgid+">")

	m.SetBody("text/plain", string(b))

	sb := &bytes.Buffer{}
	m.WriteTo(sb)
	os.Stderr.Write(sb.Bytes())

	ac := aws.NewConfig().WithRegion(*optRegion)
	sess := session.Must(session.NewSessionWithOptions(
		session.Options{
			Config:            *ac,
			SharedConfigState: session.SharedConfigEnable,
		}))

	cl := ses.New(sess)
	in := &ses.SendRawEmailInput{
		Destinations:  dests,
		FromArn:       nil,
		RawMessage:    &ses.RawMessage{Data: sb.Bytes()},
		ReturnPathArn: nil,
		Source:        nil,
		SourceArn:     nil,
	}

	if *optConfigurationSet != "" {
		in.ConfigurationSetName = optConfigurationSet
	}

	out, err := cl.SendRawEmail(in)
	if err != nil {
		log.Fatalf("*** SendEmail: %v", err)
	}

	fmt.Fprintf(os.Stderr, "%s\n", out)
}

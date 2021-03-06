// Copyright (c) 2019 Ryan Young
//
// The MIT License (MIT)
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package main

import (
	"bufio"
	"bytes"
	"crypto/hmac"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"flag"
	"io/ioutil"
	"log"
	"net"
	"net/mail"
	"os"
	"strings"
	"time"

	"github.com/delucks/smtpd"
	"github.com/gregdel/pushover"
)

// An Envelope represents an email that is finalized, parsed, and ready for
// submission.
type Envelope struct {
	From string
	To   string
	Msg  *mail.Message
}

// SendPushover converts an Envelope into a Pushover notification. In the event
// of an error condition, retryable indicates whether or not the Envelope can be
// resent.
func SendPushover(e *Envelope, api *pushover.Pushover) (retryable bool, err error) {
	sub := e.Msg.Header.Get("Subject")
	if sub == "" {
		sub = "(no subject)"
	}
	body, err := ioutil.ReadAll(e.Msg.Body)
	if err != nil {
		retryable = false
		return
	}
	user, _ := parseEmail(e.To)
	rcpt := pushover.NewRecipient(user)
	_, err = api.GetRecipientDetails(rcpt)
	if err != nil {
		retryable = false
		return
	}

	push := pushover.NewMessageWithTitle(string(body), sub+" ("+e.From+")")
	resp, err := api.SendMessage(push, rcpt)
	if err != nil {
		retryable = resp != nil && resp.Status != 1
		return
	}
	retryable = false
	return
}

// Config holds all parameters for SMTP Translator.
type Config struct {
	Addr        string
	AuthDb      map[string]string
	Hostname    string
	TLSCert     string
	TLSKey      string
	Starttls    bool
	StarttlsReq bool

	PushoverToken string
	PushoverRcpt  string
}

// ListenAndServe runs an instance of SMTP Translator. It takes a server
// configuration and a logger for non-fatal errors.
func ListenAndServe(c *Config, errl *log.Logger) error {
	q := make(chan *Envelope, 10)
	api := pushover.New(c.PushoverToken)
	server := smtpd.Server{
		Addr:         c.Addr,
		Appname:      "SMTP-Translator",
		AuthRequired: len(c.AuthDb) > 0,
		Hostname:     c.Hostname,
		MaxSize:      1024 * 4, // per https://pushover.net/api#limits
		TLSListener:  !c.Starttls && !c.StarttlsReq,
		TLSRequired:  c.StarttlsReq,
		AuthHandler: func(remoteAddr net.Addr, mechanism string, username []byte, password []byte, shared []byte) (bool, error) {
			if len(c.AuthDb) <= 0 {
				return true, nil
			}
			switch mechanism {
			case "PLAIN", "LOGIN":
				return authPlaintext(c.AuthDb, string(username), string(password)), nil
			case "CRAM-MD5":
				// username = username, password = hmac, shared = challenge
				// (see github.com/mhale/smtpd/smtpd.go)
				return authCramMd5(c.AuthDb, string(username), password, shared)
			}
			panic(mechanism)
		},
		HandlerRcpt: func(remoteAddr net.Addr, from string, to string) bool {
			_, dom := parseEmail(to)
			switch dom {
			case "api.pushover.net", "pomail.net":
				return true
			default:
				return false
			}
		},
		Handler: func(remoteAddr net.Addr, from string, to []string, data []byte) {
			msg, err := mail.ReadMessage(bytes.NewReader(data))
			if err != nil {
				return
			}
			for _, rcpt := range to {
				_, dom := parseEmail(rcpt)
				switch dom {
				case "api.pushover.net", "pomail.net":
					q <- &Envelope{
						From: from,
						To:   rcpt,
						Msg:  msg}
				default:
					errl.Println("bad domain in address:", dom)
				}
			}
		}}
	if c.TLSCert != "" && c.TLSKey != "" {
		if err := server.ConfigureTLS(c.TLSCert, c.TLSKey); err != nil {
			return err
		}
	}
	go func() {
		for {
			var e *Envelope = <-q
			for {
				retry, err := SendPushover(e, api)
				if err != nil && retry {
					errl.Println(err, "(retrying in 10 seconds)")
					time.Sleep(10 * time.Second)
					continue
				} else if err != nil {
					errl.Println(err, "(not recoverable)")
				}
				break
			}
		}
	}()
	return server.ListenAndServe()
}

func authPlaintext(db map[string]string, user, pw string) bool {
	return db[user] != "" && db[user] == pw
}

// authCramMd5 implements the CRAM-MD5 SMTP authentication method, which compares
// a user-submitted HMAC with an expected HMAC that is derived from a shared
// secret (in SMTP Translator's case, the plaintext password).
func authCramMd5(db map[string]string, user string, mac, chal []byte) (bool, error) {
	if db[user] == "" {
		return false, nil
	}
	// https://en.wikipedia.org/wiki/CRAM-MD5#Protocol
	rec := make([]byte, hex.DecodedLen(len(mac)))
	n, err := hex.Decode(rec, mac)
	if err != nil {
		return false, err
	}
	rec = rec[:n]
	mymac := hmac.New(md5.New, []byte(db[user]))
	mymac.Write(chal)
	exp := mymac.Sum(nil)
	return hmac.Equal(exp, rec), nil
}

func parseEmail(addr string) (user string, dom string) {
	spl := strings.SplitN(addr, "@", 2)
	if len(spl) != 2 {
		return "", ""
	}
	return spl[0], spl[1]
}

func main() {
	errl := log.New(os.Stdout, "", 0)
	c, err := getConfig()
	if err != nil {
		errl.Println(err)
		return
	}
	errl.Println(ListenAndServe(c, errl))
}

func getConfig() (*Config, error) {
	addr := flag.String("addr", ":25",
		"address:port to listen on")
	authp := flag.String("auth", "",
		"authenticate senders with username:password combinations from `file`")
	oshost, err := os.Hostname()
	if err != nil {
		oshost = "localhost"
	}
	host := flag.String("hostname", oshost,
		"advertise an SMTP server hostname")
	tlsCert := flag.String("tls-cert", "",
		"if using TLS, path to TLS certificate file")
	tlsKey := flag.String("tls-key", "",
		"if using TLS, path to TLS key file")
	starttls := flag.Bool("starttls", false,
		"if using TLS, accept unencrypted connections that may upgrade with STARTTLS")
	starttlsReq := flag.Bool("starttls-always", false,
		"if using TLS, accept unencrypted connections that MUST upgrade with STARTTLS")
	flag.Parse()

	if (*tlsCert != "" || *tlsKey != "") && (*tlsCert == "" || *tlsKey == "") {
		return nil, errors.New("must specify both -tls-cert and -tls-key")
	}
	if *starttls && *starttlsReq {
		return nil, errors.New("must specify either -starttls or -starttls-always")
	}
	if (*starttls || *starttlsReq) && (*tlsCert == "" || *tlsKey == "") {
		return nil, errors.New("must specify -tls-cert and -tls-key to use TLS")
	}
	token, ok := os.LookupEnv("PUSHOVER_TOKEN")
	if !ok {
		return nil, errors.New("missing env: $PUSHOVER_TOKEN")
	}

	var authdb map[string]string
	if *authp != "" {
		authf, err := os.Open(*authp)
		if err != nil {
			return nil, err
		}
		authdb, err = readAuth(authf)
		authf.Close()
		if err != nil {
			return nil, err
		}
	}

	return &Config{
		Addr:        *addr,
		AuthDb:      authdb,
		Hostname:    *host,
		TLSCert:     *tlsCert,
		TLSKey:      *tlsKey,
		Starttls:    *starttls,
		StarttlsReq: *starttlsReq,

		PushoverToken: token}, nil
}

func readAuth(fd *os.File) (db map[string]string, err error) {
	db = make(map[string]string)
	scanner := bufio.NewScanner(fd)
	for scanner.Scan() {
		split := strings.Split(scanner.Text(), ":")
		if len(split) == 2 {
			db[split[0]] = split[1]
		}
	}
	err = scanner.Err()
	return
}

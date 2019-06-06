package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io/ioutil"
	"net/http"
	"os"
	"regexp"
	"strconv"

	"github.com/azer/logger"
	"github.com/coreos/pkg/flagutil"
	"github.com/dghubble/go-twitter/twitter"
	"github.com/dghubble/oauth1"
	"github.com/gorilla/mux"
	"github.com/rs/cors"
)

var rootRouter = mux.NewRouter().PathPrefix("/verified").Subrouter()

var log = logger.New("verify")

func init() {
	rootRouter.NotFoundHandler = http.HandlerFunc(handle404)
	rootRouter.HandleFunc("/publisher/check/{id:[a-f0-9]{64}}", handleCheck)
}

func RespondJSON(w http.ResponseWriter, code int, payload interface{}) {
	b, err := json.Marshal(payload)
	if err != nil {
		log.Error("Unable to marshal response payload", logger.Attrs{"err": err, "payload": payload})
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(500)
		n, err := w.Write([]byte("Internal server error"))
		if err != nil {
			log.Error("Unable to write json response", logger.Attrs{"n": n, "err": err, "payload": payload, "code": code})
		}
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	n, err := w.Write(b)
	if err != nil {
		log.Error("Unable to write json response", logger.Attrs{"n": n, "err": err, "payload": payload, "code": code})
	}
}

func handleCheck(w http.ResponseWriter, r *http.Request) {
	var opts = mux.Vars(r)

	var nameTwitter, txidTwitter string

	status := VerificationResponse{}

	vc, err := getVerificationClaim(opts["id"])
	if err != nil {
		status.Msg = "Unable to locate verification claim with ID " + opts["id"]
		RespondJSON(w, 200, status)
		return
	}

	if len(vc.TwitterId) == 0 {
		status.TwitterMsg = "No tweet ID provided"
	} else {
		nameTwitter, txidTwitter, err = getTwitter(client, vc.TwitterId)
		if err != nil {
			if err == ErrBadFormat {
				status.TwitterMsg = "Tweet contents not properly formatted"
			} else {
				status.TwitterMsg = "Unable to locate tweet with ID " + vc.TwitterId
			}
		}

		pubTwitter, err := getPublisher(txidTwitter)
		if err != nil {
			status.TwitterMsg = "Unable to locate publisher with ID" + txidTwitter
		} else {
			if pubTwitter.Name != nameTwitter {
				status.TwitterMsg = "Claimed name doesn't match publisher name"
			}
		}
	}

	if len(vc.GabId) == 0 {
		status.GabMsg = "No post ID provided"
	} else {
		nameGab, txidGab, err := getGab(vc.GabId)
		if err != nil {
			if err == ErrBadFormat {
				status.GabMsg = "Post contents not properly formatted"
			} else {
				status.GabMsg = "Unable to locate post with ID " + vc.GabId
			}
		}

		if nameGab != nameTwitter || txidGab != txidTwitter {
			pubGab, err := getPublisher(txidGab)
			if err != nil {
				status.GabMsg = "Unable to locate publisher with ID " + txidGab
			} else {
				if pubGab.Name != nameTwitter {
					status.GabMsg = "Claimed name doesn't match publisher name"
				}
			}
		}
	}

	if len(status.TwitterMsg) == 0 {
		status.Twitter = true
	}

	if len(status.GabMsg) == 0 {
		status.Gab = true
	}

	RespondJSON(w, 200, status)
}

func Serve() {
	err := http.ListenAndServe(":1607", cors.Default().Handler(rootRouter))
	if err != nil {
		log.Error("Error serving http api", logger.Attrs{"err": err, "listen": ":1607"})
	}
}

func handle404(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte("404 not found"))
	log.Info("404", logger.Attrs{
		"url":           r.URL,
		"httpMethod":    r.Method,
		"remoteAddr":    r.RemoteAddr,
		"contentLength": r.ContentLength,
		"userAgent":     r.UserAgent(),
	})
}

var (
	client *twitter.Client
)

func main() {
	flags := flag.NewFlagSet("user-auth", flag.ContinueOnError)
	consumerKey := flags.String("consumer-key", "", "Twitter Consumer Key")
	consumerSecret := flags.String("consumer-secret", "", "Twitter Consumer Secret")
	accessToken := flags.String("access-token", "", "Twitter Access Token")
	accessSecret := flags.String("access-secret", "", "Twitter Access Secret")
	err := flags.Parse(os.Args[1:])
	if err != nil {
		panic(err)
	}
	err = flagutil.SetFlagsFromEnv(flags, "TWITTER")
	if err != nil {
		panic(err)
	}

	if *consumerKey == "" || *consumerSecret == "" || *accessToken == "" || *accessSecret == "" {
		panic("Consumer key/secret and Access token/secret required")
	}

	config := oauth1.NewConfig(*consumerKey, *consumerSecret)
	token := oauth1.NewToken(*accessToken, *accessSecret)
	httpClient := config.Client(context.Background(), token)

	client = twitter.NewClient(httpClient)

	Serve()
}

func getVerificationClaim(txid string) (*VerificationClaim, error) {
	body, err := httpGet("https://api.oip.io/oip/o5/record/get/" + txid)
	if err != nil {
		return nil, err
	}

	results := &oipApiResult{}
	err = json.Unmarshal(body, results)
	if err != nil {
		return nil, err
	}

	if len(results.Results) == 1 {
		return &results.Results[0].Record.Details.VerificationClaim, nil
	}

	return nil, errors.New("unable to find verification claim by txid")
}

func getTwitter(client *twitter.Client, id string) (name string, txid string, err error) {
	intId, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", "", err
	}
	tweet, _, err := client.Statuses.Show(intId, nil)
	if err != nil {
		return "", "", err
	}
	tweetTokens := verificationRegex.FindStringSubmatch(tweet.Text)
	if len(tweetTokens) != 3 {
		return "", "", ErrBadFormat
	}
	return tweetTokens[1], tweetTokens[2], nil
}

func httpGet(url string) ([]byte, error) {
	res, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func getGab(postId string) (name string, txid string, err error) {
	body, err := httpGet("https://gab.com/posts/" + postId)
	if err != nil {
		return "", "", err
	}

	gp := &gabPost{}
	err = json.Unmarshal(body, gp)
	if err != nil {
		return "", "", err
	}
	gabTokens := verificationRegex.FindStringSubmatch(gp.Body)

	if len(gabTokens) != 3 {
		return "", "", ErrBadFormat
	}

	return gabTokens[1], gabTokens[2], nil
}

func getPublisher(txid string) (*Publisher, error) {
	body, err := httpGet("https://api.oip.io/oip/o5/record/get/" + txid)
	if err != nil {
		return nil, err
	}

	results := &oipApiResult{}
	err = json.Unmarshal(body, results)
	if err != nil {
		return nil, err
	}

	if len(results.Results) == 1 {
		return &results.Results[0].Record.Details.Publisher, nil
	}

	return nil, errors.New("unable to find publisher by txid")
}

type gabPost struct {
	Body string `json:"body"`
}

type elasticOip5Record struct {
	Record record `json:"record"`
	Meta   RMeta  `json:"meta"`
}

type RMeta struct {
	Deactivated bool   `json:"deactivated"`
	SignedBy    string `json:"signed_by"`
	Time        int64  `json:"time"`
	Txid        string `json:"txid"`
}

type oipApiResult struct {
	Count   int
	Total   int
	Results []elasticOip5Record
	After   string
}

type record struct {
	Details details `json:"details"`
}

type details struct {
	Publisher         Publisher         `json:"tmpl_433C2783"`
	VerificationClaim VerificationClaim `json:"tmpl_F471DFF9"`
}

type tmpl433C2783 struct {
	Name         string `json:"name"`
	FloBip44XPub string `json:"floBip44XPub"`
}

type tmplF471DFF9 struct {
	GabId     string `json:"gabId"`
	TwitterId string `json:"twitterId"`
	// RegisteredPublisher string `json:"registeredPublisher"`
}

type VerificationClaim struct {
	tmplF471DFF9
}

type Publisher struct {
	tmpl433C2783
}

var verificationRegex = regexp.MustCompile(`@OpenIndexProto(?:col)?\p{Zs}verifying\p{Zs}"(.+)"\p{Zs}is\p{Zs}publishing\p{Zs}as:\p{Zs}\n?([0-9a-f]{64})`)

type VerificationResponse struct {
	Twitter    bool   `json:"twitter"`
	TwitterMsg string `json:"twitter_msg,omitempty"`
	Gab        bool   `json:"gab"`
	GabMsg     string `json:"gab_msg,omitempty"`
	Msg        string `json:"msg,omitempty"`
}

var ErrBadFormat = errors.New("message contents did not match expected format")

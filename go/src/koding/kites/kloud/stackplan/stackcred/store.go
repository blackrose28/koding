package stackcred

import (
	"fmt"
	"net/http"
	"net/url"

	"koding/db/mongodb"
	"koding/kites/kloud/utils/object"

	"github.com/koding/logging"
)

// NotFoundError represents an error fetching credentials.
//
// Identfiers of credentials that are missing in the underlying
// storage are listed in the Identifiers field.
type NotFoundError struct {
	Identifiers []string
	Err         error
}

// Error implements the built-in error interface.
func (e *NotFoundError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%v credentials not found due to the error: %s", e.Identifiers, e.Err)
	}

	return fmt.Sprintf("%v credentials not found", e.Identifiers)
}

// Fetcher provides an interface for fetching credentials.
type Fetcher interface {
	// Fetch obtains credentials from the underlying store for the given
	// username and credential identifiers.
	//
	// The creds parameters maps identifier string to a credential
	// data value.
	//
	// If the data value is non-nil, the method will decode fetched
	// credential data from the underlying representation (e.g. bson.M
	// for MongoDB credentials store) to the given value. The behaviour
	// for the decoding can be altered by providing custom ObjectBuilder
	// via StoreOptions param. By default object.HCLBuilder decoding
	// is used. If the data value implements kloud.Validator interface,
	// it will be used to ensure decoding was successful.
	//
	// If the data value is nil, the fetcher will use store-specific
	// representation. This is used to defer decoding of the data
	// value, leaving it to the caller.
	Fetch(username string, creds map[string]interface{}) error
}

// Putter provides an interface for inserting/updating credentials.
type Putter interface {
	Put(username string, creds map[string]interface{}) error
}

// StoreOptions are used to alter default behavior of credential store
// implementations.
type StoreOptions struct {
	MongoDB       *mongodb.MongoDB
	Log           logging.Logger
	CredURL       *url.URL
	ObjectBuilder *object.Builder
	Client        *http.Client
}

func (opts *StoreOptions) objectBuilder() *object.Builder {
	if opts.ObjectBuilder != nil {
		return opts.ObjectBuilder
	}

	return object.HCLBuilder
}

func (opts *StoreOptions) new(logName string) *StoreOptions {
	optsCopy := *opts
	optsCopy.Log = opts.Log.New(logName)

	return &optsCopy
}

// NewStore gives new credential store for the given options.
//
// The returned fetcher tries to fetch credential datas from sneaker,
// for each missing credential it fallbacks to Mongo and on each
// successful fetch from Mongo it updates the credential data back
// on sneaker.
func NewStore(opts *StoreOptions) Fetcher {
	social := &socialStore{
		StoreOptions: opts.new("social"),
	}

	return &FallbackFetcher{
		Fetchers: []Fetcher{
			social,
			&TeeFetcher{
				Fetcher: &mongoStore{
					StoreOptions: opts.new("mongo"),
				},
				Putter: social,
				Log:    opts.Log.New("TeeFetcher"),
			},
		},
		Log: opts.Log.New("FallbackFetcher"),
	}
}

func toIdents(creds map[string]interface{}) []string {
	idents := make([]string, 0, len(creds))
	for ident := range creds {
		idents = append(idents, ident)
	}

	return idents
}

// FallbackFetcher fetches credential datas recovering from *NotFoundError
// by fetching missing ones with next fetcher until nil error is returned.
type FallbackFetcher struct {
	Fetchers []Fetcher
	Log      logging.Logger
}

// Fetch implements the Fetcher interface than handles *NotFoundError by
// trying to fetch missing credentials from the next fetcher from
// the Fetchers slice.
func (ff *FallbackFetcher) Fetch(username string, creds map[string]interface{}) error {
	left := creds

	for i, f := range ff.Fetchers {
		if len(left) == 0 {
			break
		}

		err := f.Fetch(username, left)
		e, ok := err.(*NotFoundError)

		ff.Log.Debug("%d: fetch result: left=%+v, err=%+v", i, left, err)

		if err != nil && !ok {
			// errors other than *NotFoundError can't be recovered from
			return err
		}

		// ensure all fetched data values are updated in creds map
		if err == nil || ok {
			for ident, data := range left {
				// ensure the ident was requested so we do not leak
				// excessive data values
				if _, ok := creds[ident]; ok {
					creds[ident] = data
				}
			}
		}

		left = make(map[string]interface{})

		if err == nil {
			break
		}

		for _, ident := range e.Identifiers {
			data, ok := creds[ident]
			if !ok {
				// this should not happen, can't have not found error
				// for a credential that was not requested; safe to ignore
				continue
			}

			left[ident] = data
		}
	}

	if len(left) != 0 {
		return &NotFoundError{
			Identifiers: toIdents(left),
		}
	}

	return nil
}

// TeeFetcher creates/updates with Putter each credential data
// that was fetched by Fetcher.
type TeeFetcher struct {
	Fetcher Fetcher
	Putter  Putter
	Log     logging.Logger
}

// TeeFetcher implements the Fetcher interfaces which puts each fetched
// credential data from F to P.
func (tf *TeeFetcher) Fetch(username string, creds map[string]interface{}) error {
	err := tf.Fetcher.Fetch(username, creds)
	e, ok := err.(*NotFoundError)

	tf.Log.Debug("fetch result: creds=%+v, err=%+v", creds, err)

	if err != nil && !ok {
		// early return - no credential was fetched
		return err
	}

	fetched := make(map[string]interface{})

	for ident, data := range creds {
		fetched[ident] = data
	}

	// if there are missing credential datas, remove them from fetched
	if ok && e != nil {
		for _, ident := range e.Identifiers {
			delete(fetched, ident)
		}
	}

	if len(fetched) != 0 {
		// ignore errors from Put, we're going to retry putting
		// credential data next time it's fetched
		tf.Putter.Put(username, fetched)
	}

	return err
}

package spotshot

import (
	"encoding/gob"
	"fmt"
	"html/template"
	"math/rand"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-redis/redis"
	"github.com/gorilla/sessions"
	"github.com/sirupsen/logrus"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2"
)

type SessionKey int

const (
	SpotifyState SessionKey = iota
	SpotifyUserID
	IsSubscribed
	IsLoggedIn
)

const (
	alphanumChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ01234567890"
	SessionName   = "session"
)

func RegisterGobEncodings() {
	gob.Register(SessionKey(0))
	gob.Register(&oauth2.Token{})
}

type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

type Endpoint struct {
	HandlerFunc
	Logger logrus.FieldLogger
}

func (e *Endpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	err := e.HandlerFunc(w, r)
	if err != nil {
		e.Logger.Error(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(http.StatusText(http.StatusInternalServerError)))
	}
}

func Home(homeTmpl *template.Template, store sessions.Store, logger logrus.FieldLogger) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Fetch session (or create new one if need be).
		session, err := store.Get(r, SessionName)
		if err != nil {
			logger.Warn(SessionFetchError{err})
		}

		var data struct {
			IsLoggedIn   bool
			IsSubscribed bool
		}
		data.IsLoggedIn = isLoggedIn(session)
		data.IsSubscribed, _ = session.Values[IsSubscribed].(bool)
		w.WriteHeader(200)
		homeTmpl.Execute(w, data)
		return nil
	}
}

func SpotifyLogin(auth spotify.Authenticator, store sessions.Store, logger logrus.FieldLogger) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Fetch session (or create new one if need be).
		session, err := store.Get(r, SessionName)
		if err != nil {
			logger.Warn(SessionFetchError{err})
		}

		// Generate random state string and store in session to check later in Callback.
		state := randAlphanumStr(10)
		session.Values[SpotifyState] = state

		err = session.Save(r, w)
		if err != nil {
			return fmt.Errorf("couldn't save session: %w", err)
		}

		// Redirect user to authenticate with Spotify.
		url := auth.AuthURL(state)
		http.Redirect(w, r, url, http.StatusFound)
		return nil
	}
}

func Callback(auth spotify.Authenticator, store sessions.Store, redisClient redis.UniversalClient, logger logrus.FieldLogger) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Fetch session.
		session, err := store.Get(r, SessionName)
		if err != nil {
			logger.Warn(SessionFetchError{err})
		}
		state, ok := session.Values[SpotifyState].(string)
		if !ok {
			if _, ok = session.Values[SpotifyState]; !ok {
				return ErrStateNotSet
			}
			return ErrStateUnexpectedType
		}

		// auth.Token uses the code in query params to get an OAuth token
		// from the Spotify API.
		token, err := auth.Token(state, r)
		if err != nil {
			return fmt.Errorf("couldn't get token: %w", err)
		}
		// Get user details with the new token.
		client := auth.NewClient(token)
		user, err := client.CurrentUser()
		if err != nil {
			return fmt.Errorf("err fetching curr user info: %s", err)
		}
		// Store user ID in session. We'll use this later to fetch other details from Redis.
		session.Values[SpotifyUserID] = user.ID
		// Set in session if they are subscribed or not.
		key := fmt.Sprintf("%s:%s", RedisUserIDKey, user.ID)
		session.Values[IsSubscribed], err = redisClient.HExists(key, NumSongsField).Result()
		if err != nil {
			return fmt.Errorf("couldn't get num songs: %w", err)
		}

		// Set refresh token in Redis.
		err = redisClient.HSet(key, RefreshTokenField, token.RefreshToken).Err()
		if err != nil {
			return fmt.Errorf("error while setting redis key %s: %w", RefreshTokenField, err)
		}

		session.Values[IsLoggedIn] = true

		err = session.Save(r, w)
		if err != nil {
			return fmt.Errorf("couldn't save session: %w", err)
		}

		http.Redirect(w, r, "/", http.StatusFound)
		return nil
	}
}

func Logout(store sessions.Store, logger logrus.FieldLogger) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Fetch session.
		session, err := store.Get(r, SessionName)
		if err != nil {
			logger.Warn(SessionFetchError{err})
		}
		if !isLoggedIn(session) {
			return ErrNotLoggedIn
		}

		// Delete session.
		session.Options.MaxAge = -1
		err = session.Save(r, w)
		if err != nil {
			return fmt.Errorf("couldn't save session: %w", err)
		}

		http.Redirect(w, r, r.Referer(), http.StatusFound)
		return nil
	}
}

func Subscribe(store sessions.Store, redisClient redis.UniversalClient, logger logrus.FieldLogger) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Fetch session.
		session, err := store.Get(r, SessionName)
		if err != nil {
			logger.Warn(SessionFetchError{err})
		}
		if !isLoggedIn(session) {
			return ErrNotLoggedIn
		}
		// Get user ID from session.
		userID, ok := session.Values[SpotifyUserID].(string)
		if !ok {
			if _, ok = session.Values[SpotifyUserID]; !ok {
				return ErrUserIDNotSet
			}
			return UserIDUnexpectedTypeError{session.Values[SpotifyUserID]}
		}

		// Parse num_songs from form data.
		nStr := r.FormValue("num_songs")
		if nStr == "" {
			return ExpectedFormValueError{"num_songs"}
		}
		n, err := strconv.Atoi(nStr)
		if err != nil {
			return fmt.Errorf("couldn't convert num_songs to int: %w", err)
		}
		if n > 50 {
			n = 50
		}
		isPrivate := true
		if r.FormValue("is_private") == "" {
			isPrivate = false
		}

		HSetIfNoErr := func(key string, field string, value interface{}) {
			if err == nil {
				err = redisClient.HSet(key, field, value).Err()
			}
		}
		key := fmt.Sprintf("%s:%s", RedisUserIDKey, userID)
		HSetIfNoErr(key, NumSongsField, n)
		// Redis doesn't have booleans. Let's just have the existence of the key indicate true.
		if isPrivate {
			HSetIfNoErr(key, IsPrivateField, "")
		}
		if err != nil {
			return fmt.Errorf("error while setting redis key: %w", err)
		}

		session.Values[IsSubscribed] = true
		logger.WithField("user_id", userID).Infof("subscribed")

		err = session.Save(r, w)
		if err != nil {
			return fmt.Errorf("couldn't save session: %w", err)
		}

		http.Redirect(w, r, r.Referer(), http.StatusFound)
		return nil
	}
}

func Unsubscribe(store sessions.Store, redisClient redis.UniversalClient, logger logrus.FieldLogger) HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) error {
		// Fetch session.
		session, err := store.Get(r, SessionName)
		if err != nil {
			logger.Warn(SessionFetchError{err})
		}
		if !isLoggedIn(session) {
			return ErrNotLoggedIn
		}
		// Get user ID from session.
		userID, ok := session.Values[SpotifyUserID].(string)
		if !ok {
			if _, ok = session.Values[SpotifyUserID]; !ok {
				return ErrUserIDNotSet
			}
			return UserIDUnexpectedTypeError{session.Values[SpotifyUserID]}
		}

		key := fmt.Sprintf("%s:%s", RedisUserIDKey, userID)
		err = redisClient.HDel(key, NumSongsField).Err()
		if err != nil {
			return fmt.Errorf("couldn't delete redis field %s in key %s: %w", NumSongsField, key, err)
		}

		session.Values[IsSubscribed] = false
		logger.WithField("user_id", userID).Infof("unsubscribed")

		err = session.Save(r, w)
		if err != nil {
			return fmt.Errorf("couldn't save session: %w", err)
		}

		http.Redirect(w, r, r.Referer(), http.StatusFound)
		return nil
	}
}

func randAlphanumStr(n int) string {
	var sb strings.Builder
	for i := 0; i < n; i++ {
		r := rand.Intn(len(alphanumChars))
		sb.WriteByte(alphanumChars[r])
	}
	return sb.String()
}

func isLoggedIn(session *sessions.Session) bool {
	if session == nil {
		return false
	}
	isLoggedIn, _ := session.Values[IsLoggedIn].(bool)
	return isLoggedIn
}

package spotshot

import (
	"context"
	"fmt"
	"io/ioutil"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis"
	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2"
)

type mockSpotifyClient struct {
	playlists []playlist
}

type playlist struct {
	user   string
	name   string
	desc   string
	public bool
	tracks []spotify.ID
}

func (m *mockSpotifyClient) CurrentUsersTopTracksOpt(opts *spotify.Options) (*spotify.FullTrackPage, error) {
	tracks := make([]spotify.FullTrack, *opts.Limit)
	for i := 0; i < *opts.Limit; i++ {
		tracks[i].ID = spotify.ID(i)
	}
	return &spotify.FullTrackPage{Tracks: tracks}, nil
}

func (m *mockSpotifyClient) CreatePlaylistForUser(user, name, desc string, public bool) (*spotify.FullPlaylist, error) {
	m.playlists = append(m.playlists, playlist{user, name, desc, public, make([]spotify.ID, 0)})
	fp := &spotify.FullPlaylist{}
	fp.ID = spotify.ID(0)
	return fp, nil
}

// adds to the latest playlist
func (m *mockSpotifyClient) AddTracksToPlaylist(playlistID spotify.ID, trackIDs ...spotify.ID) (string, error) {
	tracks := &m.playlists[len(m.playlists)-1].tracks
	*tracks = append(*tracks, trackIDs...)
	return "", nil
}

func (m *mockSpotifyClient) clear() {
	m.playlists = make([]playlist, 0)
}

type motherOfSpotClients struct {
	msc *mockSpotifyClient
}

func (m *motherOfSpotClients) mockSpotifyClientCreator(spotify.Authenticator) func(*oauth2.Token) SpotifyClienter {
	return func(token *oauth2.Token) SpotifyClienter {
		m.msc = &mockSpotifyClient{}
		return m.msc
	}
}

func TestPlaylistCreatorSubscribedUser(t *testing.T) {
	// t.Parallel()
	// Test creation of a playlist at the start of the month for a subscribed user.
	s, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis run failed: %s", err)
	}
	defer s.Close()
	numSongs := 50
	user := "coolkid99"
	key := fmt.Sprintf("%s:%s", RedisUserIDKey, user)
	s.HSet(key, NumSongsField, strconv.Itoa(numSongs))
	s.HSet(key, RefreshTokenField, "test")

	redisClient := redis.NewClient(&redis.Options{
		Addr: s.Addr(),
	})
	err = redisClient.Ping().Err()
	if err != nil {
		t.Fatalf("error connecting to redis: %s", err)
	}
	// No logging.
	logger := logrus.New()
	logger.Out = ioutil.Discard

	// Set timeNow to return a time that is initially offset to 25ms before new month.
	now := time.Now()
	nextMonth := now.Month() + 1
	if nextMonth == 13 {
		nextMonth = 1
	}
	nextMonthTime := time.Date(now.Year(), nextMonth, 1, 0, 0, 0, 0, now.Location())
	offset := nextMonthTime.Sub(now) - 25*time.Millisecond
	timeNow = func() time.Time {
		return time.Now().Add(offset)
	}
	// Set PlaylistCreator to check for a new month evey 5 milliseconds, and set it to die via the context after 50 milliseconds.
	monthCheckFreq = 5 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	mother := new(motherOfSpotClients)
	playlistNowCh := make(chan spotify.ID)
	PlaylistCreator(ctx, redisClient, logger, mother.mockSpotifyClientCreator(spotify.Authenticator{}), playlistNowCh)
	close(playlistNowCh)

	if mother.msc == nil {
		t.Fatalf("expected spotify client to be created")
	}
	if len(mother.msc.playlists) != 1 {
		t.Fatalf("expected 1 playlist, got %d", len(mother.msc.playlists))
	}
	if mother.msc.playlists[0].public != true {
		t.Errorf("expected public playlist, got private")
	}
	if mother.msc.playlists[0].user != user {
		t.Errorf("expected user %s, got %s", user, mother.msc.playlists[0].user)
	}
	if len(mother.msc.playlists[0].tracks) != numSongs {
		t.Errorf("expected %d songs, got %d", numSongs, len(mother.msc.playlists[0].tracks))
	}
	match, err := regexp.MatchString(`Your Top Songs (Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Oct|Nov|Dec) \d{2}`, mother.msc.playlists[0].name)
	if err != nil {
		t.Fatalf("couldn't compile regex: %s", err)
	}
	if !match {
		t.Errorf("playlist name does not match expected format, e.g. Aug 19, got %s", mother.msc.playlists[0].name)
	}
	match, err = regexp.MatchString(`Your top songs in (January|February|March|April|May|June|July|August|September|October|November|December) \d{4}, made by `+DomainName, mother.msc.playlists[0].desc)
	if err != nil {
		t.Fatalf("couldn't compile regex: %s", err)
	}
	if !match {
		t.Errorf("playlist desc does not match expected format, got %s", mother.msc.playlists[0].desc)
	}
}

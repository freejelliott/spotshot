package spotshot

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-redis/redis"
	"github.com/sirupsen/logrus"
	"github.com/zmb3/spotify"
	"golang.org/x/oauth2"
)

const (
	RedisUserIDKey    = "spot_usr_id"
	NumSongsField     = "num_songs"
	RefreshTokenField = "refresh_token"
	IsPrivateField    = "is_private"
	DomainName        = "spotshot.jelliott.dev"
)

var (
	timeNow        = time.Now
	monthCheckFreq = time.Minute
)

type SpotifyClienter interface {
	CurrentUsersTopTracksOpt(opts *spotify.Options) (*spotify.FullTrackPage, error)
	CreatePlaylistForUser(user, playlistName, desc string, public bool) (*spotify.FullPlaylist, error)
	AddTracksToPlaylist(playlistID spotify.ID, trackIDs ...spotify.ID) (string, error)
}

func SpotifyClientCreator(auth spotify.Authenticator) func(*oauth2.Token) SpotifyClienter {
	return func(token *oauth2.Token) SpotifyClienter {
		client := auth.NewClient(token)
		return &client
	}
}

// PlaylistCreator will check every hour for Spotify users to create playlists for.
// Will only return if the given context is done.
func PlaylistCreator(ctx context.Context, redisClient redis.UniversalClient, logger logrus.FieldLogger, GetSpotifyClient func(token *oauth2.Token) SpotifyClienter, playlistNowCh <-chan spotify.ID) {
	curMonth := timeNow().Month()
	for {
		// Periodically check if it's a new month.
		select {
		case <-time.After(monthCheckFreq):
			if timeNow().Month() == curMonth {
				continue
			}
			// New month!
			curMonth = timeNow().Month()
		case userID := <-playlistNowCh:
			// Make a one-off playlist for the user.
			key := fmt.Sprintf("%s:%s", RedisUserIDKey, userID)
			logger = logger.WithField("user_id", userID)
			err := createPlaylist(key, true, redisClient, logger, GetSpotifyClient)
			if err != nil {
				logger.Error(err)
			}
			continue
		case <-ctx.Done():
			return
		}

		logger.Infof("creating playlists")

		keys, err := redisClient.Keys(fmt.Sprintf("%s:*", RedisUserIDKey)).Result()
		if err != nil {
			logger.Errorf("couldn't get redis keys: %s", err)
			continue
		}
		for _, key := range keys {
			userID := spotify.ID(strings.Split(key, ":")[1])
			logger = logger.WithField("user_id", userID)
			err = createPlaylist(key, false, redisClient, logger, GetSpotifyClient)
			if err != nil {
				logger.Error(err)
				continue
			}
		}
	}
}

func createPlaylist(key string, isOneOff bool, redisClient redis.UniversalClient, logger logrus.FieldLogger, GetSpotifyClient func(token *oauth2.Token) SpotifyClienter) error {
	creationType := "monthly"
	if isOneOff {
		creationType = "one-off"
	}
	logger.Infof("creating %s playlist", creationType)

	// If NumSongsField doesn't exist then they aren't subscribed.
	exists, err := redisClient.HExists(key, NumSongsField).Result()
	if err != nil {
		return fmt.Errorf("couldn't get num songs: %w", err)
	}
	if !exists {
		logger.Info("ignore playlist creation since not subscribed")
		return nil
	}

	// Get privacy setting for new playlists.
	isPrivate, err := redisClient.HExists(key, IsPrivateField).Result()
	if err != nil {
		return fmt.Errorf("couldn't get privacy field: %w", err)
	}

	// Fetch the refresh token and make a new Spotify client.
	// The client takes care of getting a new access token.
	token := new(oauth2.Token)
	token.RefreshToken, err = redisClient.HGet(key, RefreshTokenField).Result()
	if err != nil {
		return fmt.Errorf("couldn't get refresh token: %w", err)
	}
	spotClient := GetSpotifyClient(token)

	// Get numsongs-many top tracks for past month.
	numSongs, err := redisClient.HGet(key, NumSongsField).Int()
	if err != nil {
		return fmt.Errorf("couldn't get num songs: %w", err)
	}
	timerange := "short" // Approx. 4 weeks.
	opts := &spotify.Options{
		Timerange: &timerange,
		Limit:     &numSongs,
	}
	fullTrackPage, err := spotClient.CurrentUsersTopTracksOpt(opts)
	if err != nil {
		return fmt.Errorf("err fetching curr user top tracks: %w", err)
	}

	// Playlist name will look like "Aug 19".
	now := timeNow()
	year := now.Year()
	month := now.Month()
	// If it's not a one-off change the month for playlist name to previous month.
	if !isOneOff {
		month--
		if month == 0 { // New year.
			year--
			month = 12
		}
	}
	monthShort := month.String()[:3]
	yearShort := year % 100
	playlistName := fmt.Sprintf("Your Top Songs %s %0.2d", monthShort, yearShort)
	if isOneOff {
		playlistName = fmt.Sprintf("Your Monthly Top Songs %s %d %d", monthShort, now.Day(), year)
	}
	playlistDesc := fmt.Sprintf("Your top songs in %s %d, made by %s", month, year, DomainName)
	if isOneOff {
		playlistDesc = fmt.Sprintf("Your top songs in the past month before %s %d %d, made by %s", month, now.Day(), year, DomainName)
	}
	// Make the playlist! It will be empty at first.
	// TODO: support custom naming playlists
	userID := strings.Split(key, ":")[1]
	fullPlaylist, err := spotClient.CreatePlaylistForUser(userID, playlistName, playlistDesc, !isPrivate)
	if err != nil {
		return fmt.Errorf("err creating playlist for user: %w", err)
	}
	// Add all the user's top tracks to the new playlist.
	trackIDs := make([]spotify.ID, len(fullTrackPage.Tracks))
	for i, track := range fullTrackPage.Tracks {
		trackIDs[i] = track.ID
	}
	_, err = spotClient.AddTracksToPlaylist(fullPlaylist.ID, trackIDs...)
	if err != nil {
		return fmt.Errorf("err adding tracks to playlist: %w", err)
	}

	logger.Infof("created %s playlist", creationType)
	return nil
}

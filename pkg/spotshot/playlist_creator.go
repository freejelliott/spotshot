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
func PlaylistCreator(ctx context.Context, redisClient redis.UniversalClient, logger logrus.FieldLogger, GetSpotifyClient func(token *oauth2.Token) SpotifyClienter) {
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
			userID := strings.Split(key, ":")[1]
			logger := logger.WithField("user_id", userID)

			// If NumSongsField doesn't exist then they aren't subscribed.
			exists, err := redisClient.HExists(key, NumSongsField).Result()
			if err != nil {
				logger.Errorf("couldn't get num songs: %s", err)
				continue
			}
			if !exists {
				logger.Infof("ignore playlist creation since not subscribed")
				continue
			}

			// Get privacy setting for new playlists.
			isPrivate, err := redisClient.HExists(key, IsPrivateField).Result()
			if err != nil {
				logger.Errorf("couldn't get privacy field: %s", err)
				continue
			}

			// Fetch the refresh token and make a new Spotify client.
			// The client takes care of getting a new access token.
			token := new(oauth2.Token)
			token.RefreshToken, err = redisClient.HGet(key, RefreshTokenField).Result()
			if err != nil {
				logger.Errorf("couldn't get refresh token: %s", err)
				continue
			}
			spotClient := GetSpotifyClient(token)

			// Get numsongs-many top tracks for past month.
			numSongs, err := redisClient.HGet(key, NumSongsField).Int()
			if err != nil {
				logger.Errorf("couldn't get num songs: %s", err)
				continue
			}
			timerange := "short" // Approx. 4 weeks.
			opts := &spotify.Options{
				Timerange: &timerange,
				Limit:     &numSongs,
			}
			fullTrackPage, err := spotClient.CurrentUsersTopTracksOpt(opts)
			if err != nil {
				logger.Errorf("err fetching curr user top tracks: %s", err)
				continue
			}

			// Playlist name will look like "Aug 19".
			now := timeNow()
			monthShort := now.Month().String()[:3]
			yearShort := now.Year() % 100
			playlistName := fmt.Sprintf("%s %0.2d", monthShort, yearShort)
			playlistDesc := fmt.Sprintf("Your top songs for %s %d.", now.Month(), timeNow().Year())
			// Make the playlist! It will be empty at first.
			// TODO: support custom naming playlists
			fullPlaylist, err := spotClient.CreatePlaylistForUser(userID, playlistName, playlistDesc, !isPrivate)
			if err != nil {
				logger.Errorf("err creating playlist for user: %s", err)
				continue
			}
			// Add all the user's top tracks to the new playlist.
			trackIDs := make([]spotify.ID, len(fullTrackPage.Tracks))
			for i, track := range fullTrackPage.Tracks {
				trackIDs[i] = track.ID
			}
			_, err = spotClient.AddTracksToPlaylist(fullPlaylist.ID, trackIDs...)
			if err != nil {
				logger.Errorf("err adding tracks to playlist: %s", err)
				continue
			}

			logger.Infof("created monthly playlist")
		}
	}

}

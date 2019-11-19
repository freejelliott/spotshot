# Spotshot

## About
Spotshot gives you a snapshot every month of your favourite music on Spotify.

At the start of every month, it uses the Spotify API to get a list of your favourite songs for the past month, and creates a playlist of them for you.

The site is live at https://spotshot.jelliott.dev/.

## Code

Basic server configuration, setup and endpoint definitions can be found in `main.go`.

The OAuth client, as well as the other API methods are implemented in `pkg/spotshot/endpoints.go`.

The monthly playlist creator is implemented in `pkg/spotshot/playlist_creator.go`.

## Running

Recommended method of running the app is with `docker-compose`:
```
$ docker-compose up --build
```
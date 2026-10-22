package adapters

import (
	"context"
	"strings"

	"atoman/internal/model"
	"atoman/internal/modules/music"
	"atoman/internal/modules/search"
	"atoman/internal/platform/apperr"
)

type MusicProvider struct{ service *music.Service }

func NewMusicProvider(service *music.Service) *MusicProvider { return &MusicProvider{service: service} }
func (p *MusicProvider) Module() string                      { return "music" }

func (p *MusicProvider) Capabilities(_ context.Context, _ search.Viewer, scope search.Scope) search.ModuleCapabilities {
	caps := search.ModuleCapabilities{Module: p.Module(), Label: "音乐", Scope: scope}
	if scope != search.ScopePublic {
		return caps // 私人歌单、上传和收藏能力留到 P6。
	}
	for _, kind := range []struct {
		key, label string
		fields     []string
		metadata   []string
	}{
		{"song", "歌曲", []string{"title", "album", "artist", "artist_alias", "lyrics", "translation"}, []string{"artists", "album_id", "album_title", "duration_sec"}},
		{"album", "专辑", []string{"title", "artist", "artist_alias"}, []string{"artists", "release_year"}},
		{"artist", "艺术家", []string{"name", "disambiguation", "legal_name", "alias"}, []string{"disambiguation"}},
		{"playlist", "歌单", []string{"name"}, []string{"owner_id", "song_count", "bookmark_count"}},
	} {
		caps.Types = append(caps.Types, search.TypeCapability{Key: kind.key, Label: kind.label,
			Sorts: []search.SortDefinition{{Key: "relevance", Label: "相关度"}}, Filters: []search.FilterDefinition{},
			DefaultSort: "relevance", SearchFields: kind.fields, MetadataFields: kind.metadata})
	}
	return caps
}

func (p *MusicProvider) Search(ctx context.Context, _ search.Viewer, req search.ProviderSearchRequest) (search.ProviderSearchResult, error) {
	if req.Scope != search.ScopePublic {
		return search.ProviderSearchResult{}, apperr.BadRequest("search.invalid_scope", "Music private search is not available yet")
	}
	data, err := p.service.SearchCatalog(ctx, nil, music.MusicSearchOptions{Query: req.Query, Type: req.Type,
		Limit: req.Limit, Offset: req.Offset, ExactTotal: req.ExactTotal, Summary: true, Visibility: music.MusicSearchPublic})
	if err != nil {
		return search.ProviderSearchResult{}, err
	}
	result := search.ProviderSearchResult{Items: []search.SearchHit{}}
	if req.ExactTotal {
		total := data.Totals[req.Type]
		result.Total = &total
	}
	for _, song := range data.Songs {
		result.Items = append(result.Items, mapMusicSongHit(song))
	}
	for _, album := range data.Albums {
		result.Items = append(result.Items, mapMusicAlbumHit(album))
	}
	for _, artist := range data.Artists {
		result.Items = append(result.Items, mapMusicArtistHit(artist))
	}
	for _, playlist := range data.Playlists {
		result.Items = append(result.Items, mapMusicPlaylistHit(playlist))
	}
	return result, nil
}

// 仅投影公开展示字段，不将音频存储键、波形、歌词全文或模型序列化到统一响应。
func mapMusicSongHit(song model.Song) search.SearchHit {
	metadata := map[string]any{"artists": musicArtistNames(song.Artists), "duration_sec": song.DurationSec}
	if song.Album != nil {
		metadata["album_id"], metadata["album_title"] = song.Album.ID.String(), song.Album.Title
	}
	return search.SearchHit{Module: "music", Type: "song", ID: song.ID.String(), Title: song.Title,
		Subtitle: strings.Join(musicArtistNames(song.Artists), "、"), CoverURL: song.CoverURL,
		Href: "/music/song/" + song.ID.String(), Metadata: metadata}
}

func mapMusicAlbumHit(album model.Album) search.SearchHit {
	return search.SearchHit{Module: "music", Type: "album", ID: album.ID.String(), Title: album.Title,
		Subtitle: strings.Join(musicArtistNames(album.Artists), "、"), Snippet: searchSnippet(album.Description), CoverURL: album.CoverURL,
		Href: "/music/album/" + album.ID.String(), Metadata: map[string]any{"artists": musicArtistNames(album.Artists), "release_year": album.ReleaseYear}}
}

func mapMusicArtistHit(artist model.Artist) search.SearchHit {
	title := artist.DisplayName
	if title == "" {
		title = artist.Name
	}
	return search.SearchHit{Module: "music", Type: "artist", ID: artist.ID.String(), Title: title,
		Snippet: searchSnippet(artist.Bio), CoverURL: artist.ImageURL, Href: "/music/artist/" + artist.ID.String(),
		Metadata: map[string]any{"disambiguation": artist.Disambiguation}}
}

func mapMusicPlaylistHit(playlist model.Playlist) search.SearchHit {
	return search.SearchHit{Module: "music", Type: "playlist", ID: playlist.ID.String(), Title: playlist.Name,
		Snippet: searchSnippet(playlist.Description), CoverURL: playlist.CoverURL, Href: "/music/playlist/" + playlist.ID.String(),
		Metadata: map[string]any{"owner_id": playlist.UserID.String(), "song_count": playlist.SongCount, "bookmark_count": playlist.BookmarkCount}}
}

func musicArtistNames(artists []model.Artist) []string {
	names := make([]string, 0, len(artists))
	for _, artist := range artists {
		names = append(names, artist.Name)
	}
	return names
}

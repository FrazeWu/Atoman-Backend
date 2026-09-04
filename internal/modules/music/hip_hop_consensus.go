package music

// HipHopConsensusArtistSeed is a curated editorial seed for catalog imports.
// The rank is stable within the 2026 editorial-consensus list.
type HipHopConsensusArtistSeed struct {
	Rank int
	Name string
}

var hipHopConsensusEditorialSources = []string{
	"https://www.billboard.com/lists/best-rappers-all-time/",
	"https://www.rollingstone.com/music/music-lists/",
	"https://www.complex.com/music/",
}

// HipHopConsensusArtistSeeds combines the editorial lists from Billboard,
// Rolling Stone, and Complex into a stable catalog seed set.
func HipHopConsensusArtistSeeds() []HipHopConsensusArtistSeed {
	names := []string{
		"Jay-Z", "Kendrick Lamar", "Nas", "Tupac Shakur", "The Notorious B.I.G.",
		"Eminem", "Rakim", "André 3000", "Lil Wayne", "Kanye West",
		"Lauryn Hill", "Missy Elliott", "Ice Cube", "Scarface", "Black Thought",
		"Ghostface Killah", "KRS-One", "LL Cool J", "Big Daddy Kane", "Chuck D",
		"Queen Latifah", "MC Lyte", "Slick Rick", "Kool G Rap", "Big L",
		"Big Pun", "Method Man", "Redman", "Busta Rhymes", "Q-Tip",
		"Common", "Yasiin Bey", "Pusha T", "J. Cole", "Drake",
		"Nicki Minaj", "Snoop Dogg", "50 Cent", "DMX", "Ludacris",
		"T.I.", "Gucci Mane", "Future", "E-40", "Bun B",
		"Rick Ross", "Lil Kim", "Roxanne Shanté", "Rapsody", "MF DOOM",
		"GZA", "Raekwon", "RZA", "Ol' Dirty Bastard", "Masta Ace",
		"Kool Moe Dee", "Biz Markie", "MC Ren", "Eazy-E", "The D.O.C.",
		"Too $hort", "DJ Quik", "Kurupt", "Xzibit", "Warren G",
		"The Game", "Cam'ron", "Fabolous", "Fat Joe", "Nelly",
		"Twista", "Tech N9ne", "Lupe Fiasco", "Talib Kweli", "Pharoahe Monch",
		"Freddie Gibbs", "Joey Bada$$", "Tyler, The Creator", "Vince Staples", "Earl Sweatshirt",
		"Denzel Curry", "Childish Gambino", "Mac Miller", "Kid Cudi", "A$AP Rocky",
		"Travis Scott", "Young Thug", "Chief Keef", "Playboi Carti", "Lil Uzi Vert",
		"21 Savage", "Megan Thee Stallion", "Cardi B", "Remy Ma", "Foxy Brown",
		"Eve", "Trina", "Da Brat", "Project Pat", "Juicy J",
	}
	seeds := make([]HipHopConsensusArtistSeed, 0, len(names))
	for index, name := range names {
		seeds = append(seeds, HipHopConsensusArtistSeed{Rank: index + 1, Name: name})
	}
	return seeds
}

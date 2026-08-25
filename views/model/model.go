package model

type ViewsData struct {
	Card          string
	ExtraCard     string
	Title         string `default:"Instagram preview"`
	ImageURL      string `default:""`
	ImageURLs     []string
	VideoURL      string `default:""`
	PlayerURL     string `default:""`
	URL           string
	CanonicalURL  string
	Description   string
	OEmbedURL     string
	Site          string
	TwitterSite   string
	Creator       string
	OGType        string
	NoRedirect    bool
	ImageWidth    int
	ImageHeight   int
	ImageAlt      string
	FaviconURL    string
	AppleIconURL  string
	ArticleAuthor string
	Width         int `default:"400"`
	Height        int `default:"400"`
}

type OEmbedData struct {
	Text string
	URL  string
}

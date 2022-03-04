package kaas

import (
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"golang.org/x/net/html"
)

const (
	charset              = "abcdefghijklmnopqrstuvwxyz"
	randLength           = 8
	promTemplates        = "prom-templates"
	gcsLinkToken         = "gcsweb"
	gcsPrefix            = "https://gcsweb-ci.apps.ci.l2s4.p1.openshiftapps.com"
	storagePrefix        = "https://storage.googleapis.com"
	artifactsPath        = "artifacts"
	mustGatherPath       = "must-gather.tar"
	mustGatherFolderPath = "gather-must-gather"
	e2ePrefix            = "e2e"
)

func generateAppLabel() string {
	seededRand := rand.New(rand.NewSource(time.Now().UnixNano()))

	b := make([]byte, randLength)
	for i := range b {
		b[i] = charset[seededRand.Intn(len(charset))]
	}
	return string(b)
}

func getLinksFromURL(url string) ([]string, error) {
	links := []string{}

	var netClient = &http.Client{
		Timeout: time.Second * 10,
	}
	resp, err := netClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch %s: %v", url, err)
	}
	defer resp.Body.Close()

	z := html.NewTokenizer(resp.Body)
	for {
		tt := z.Next()

		switch {
		case tt == html.ErrorToken:
			// End of the document, we're done
			return links, nil
		case tt == html.StartTagToken:
			t := z.Token()

			isAnchor := t.Data == "a"
			if isAnchor {
				for _, a := range t.Attr {
					if a.Key == "href" {
						links = append(links, a.Val)
						break
					}
				}
			}
		}
	}
}

func ensureMustGatherURL(url string) (int, error) {
	var netClient = &http.Client{
		Timeout: time.Second * 10,
	}
	resp, err := netClient.Head(url)
	if resp == nil {
		return 0, err
	}
	return resp.StatusCode, err
}

func getMustGatherTar(conn *websocket.Conn, url string) (ProwInfo, error) {
	sendWSMessage(conn, "status", fmt.Sprintf("Fetching %s", url))
	// Ensure initial URL is valid
	statusCode, err := ensureMustGatherURL(url)
	if err != nil || statusCode != http.StatusOK {
		return ProwInfo{}, fmt.Errorf("failed to fetch url %s: code %d, %s", url, statusCode, err)
	}

	prowInfo, err := getTarURLFromProw(conn, url)
	if err != nil {
		return prowInfo, err
	}
	expectedMustGatherURL := prowInfo.MustGatherURL

	sendWSMessage(conn, "status", fmt.Sprintf("Found must-gather archive at %s", expectedMustGatherURL))

	// Check that metrics/prometheus.tar can be fetched and it non-null
	sendWSMessage(conn, "status", "Checking if must-gather archive can be fetched")
	var netClient = &http.Client{
		Timeout: time.Second * 10,
	}
	resp, err := netClient.Head(expectedMustGatherURL)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to fetch %s: %v", expectedMustGatherURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return prowInfo, fmt.Errorf("failed to check archive at %s: returned %s", expectedMustGatherURL, resp.Status)
	}

	contentLength := resp.Header.Get("content-length")
	if contentLength == "" {
		return prowInfo, fmt.Errorf("failed to check archive at %s: no content length returned", expectedMustGatherURL)
	}
	length, err := strconv.Atoi(contentLength)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to check archive at %s: invalid content-length: %v", expectedMustGatherURL, err)
	}
	if length == 0 {
		return prowInfo, fmt.Errorf("failed to check archive at %s: archive is empty", expectedMustGatherURL)
	}
	return prowInfo, nil
}

func getTarURLFromProw(conn *websocket.Conn, baseURL string) (ProwInfo, error) {
	prowInfo := ProwInfo{}

	// Is it a direct prom tarball link?
	if strings.HasSuffix(baseURL, mustGatherPath) {
		// Make it a fetchable URL if it's a gcsweb URL
		tempMetricsURL := strings.Replace(baseURL, gcsPrefix+"/gcs", storagePrefix, -1)
		prowInfo.MustGatherURL = tempMetricsURL
		return prowInfo, nil
	}

	// Get a list of links on prow page
	prowToplinks, err := getLinksFromURL(baseURL)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to find links at %s: %v", prowToplinks, err)
	}
	if len(prowToplinks) == 0 {
		return prowInfo, fmt.Errorf("no links found at %s", baseURL)
	}
	gcsTempURL := ""
	for _, link := range prowToplinks {
		log.Printf("link: %s", link)
		if strings.Contains(link, gcsLinkToken) {
			gcsTempURL = link
			break
		}
	}
	if gcsTempURL == "" {
		return prowInfo, fmt.Errorf("failed to find GCS link in %v", prowToplinks)
	}
	sendWSMessage(conn, "status", fmt.Sprintf("Found gcs link at %s", baseURL))

	gcsURL, err := url.Parse(gcsTempURL)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to parse GCS URL %s: %v", gcsTempURL, err)
	}

	sendWSMessage(conn, "status", fmt.Sprintf("Found GCS URL %s", gcsURL))

	// Check that 'artifacts' folder is present
	gcsToplinks, err := getLinksFromURL(gcsURL.String())
	if err != nil {
		return prowInfo, fmt.Errorf("failed to fetch top-level GCS link at %s: %v", gcsURL, err)
	}
	if len(gcsToplinks) == 0 {
		return prowInfo, fmt.Errorf("no top-level GCS links at %s found", gcsURL)
	}
	tmpArtifactsURL := ""
	for _, link := range gcsToplinks {
		if strings.HasSuffix(link, "artifacts/") {
			tmpArtifactsURL = gcsPrefix + link
			break
		}
	}
	if tmpArtifactsURL == "" {
		return prowInfo, fmt.Errorf("failed to find artifacts link in %v", gcsToplinks)
	}
	artifactsURL, err := url.Parse(tmpArtifactsURL)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to parse artifacts link %s: %v", tmpArtifactsURL, err)
	}

	// Get a list of folders in find ones which contain e2e
	artifactLinksToplinks, err := getLinksFromURL(artifactsURL.String())
	if err != nil {
		return prowInfo, fmt.Errorf("failed to fetch artifacts link at %s: %v", gcsURL, err)
	}
	if len(artifactLinksToplinks) == 0 {
		return prowInfo, fmt.Errorf("no artifact links at %s found", gcsURL)
	}
	tmpE2eURL := ""
	for _, link := range artifactLinksToplinks {
		log.Printf("link: %s", link)
		linkSplitBySlash := strings.Split(link, "/")
		lastPathSegment := linkSplitBySlash[len(linkSplitBySlash)-1]
		if len(lastPathSegment) == 0 {
			lastPathSegment = linkSplitBySlash[len(linkSplitBySlash)-2]
		}
		log.Printf("lastPathSection: %s", lastPathSegment)
		if strings.Contains(lastPathSegment, e2ePrefix) {
			tmpE2eURL = gcsPrefix + link
			break
		}
	}
	if tmpE2eURL == "" {
		return prowInfo, fmt.Errorf("failed to find e2e link in %v", artifactLinksToplinks)
	}
	e2eURL, err := url.Parse(tmpE2eURL)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to parse e2e link %s: %v", tmpE2eURL, err)
	}

	// Support new-style jobs - look for gather-extra
	var gatherExtraURL *url.URL

	e2eToplinks, err := getLinksFromURL(e2eURL.String())
	if err != nil {
		return prowInfo, fmt.Errorf("failed to fetch artifacts link at %s: %v", e2eURL, err)
	}
	if len(e2eToplinks) == 0 {
		return prowInfo, fmt.Errorf("no top links at %s found", e2eURL)
	}
	for _, link := range e2eToplinks {
		log.Printf("link: %s", link)
		linkSplitBySlash := strings.Split(link, "/")
		lastPathSegment := linkSplitBySlash[len(linkSplitBySlash)-1]
		if len(lastPathSegment) == 0 {
			lastPathSegment = linkSplitBySlash[len(linkSplitBySlash)-2]
		}
		log.Printf("lastPathSection: %s", lastPathSegment)
		if lastPathSegment == mustGatherFolderPath {
			tmpMetricsURL := gcsPrefix + link
			gatherExtraURL, err = url.Parse(tmpMetricsURL)
			if err != nil {
				return prowInfo, fmt.Errorf("failed to parse e2e link %s: %v", tmpE2eURL, err)
			}
			break
		}
	}

	if gatherExtraURL != nil {
		// New-style jobs may not have metrics available
		e2eToplinks, err = getLinksFromURL(gatherExtraURL.String())
		if err != nil {
			return prowInfo, fmt.Errorf("failed to fetch gather-must-gather link at %s: %v", e2eURL, err)
		}
		if len(e2eToplinks) == 0 {
			return prowInfo, fmt.Errorf("no top links at %s found", e2eURL)
		}
		for _, link := range e2eToplinks {
			log.Printf("link: %s", link)
			linkSplitBySlash := strings.Split(link, "/")
			lastPathSegment := linkSplitBySlash[len(linkSplitBySlash)-1]
			if len(lastPathSegment) == 0 {
				lastPathSegment = linkSplitBySlash[len(linkSplitBySlash)-2]
			}
			log.Printf("lastPathSection: %s", lastPathSegment)
			if lastPathSegment == artifactsPath {
				tmpGatherExtraURL := gcsPrefix + link
				gatherExtraURL, err = url.Parse(tmpGatherExtraURL)
				if err != nil {
					return prowInfo, fmt.Errorf("failed to parse e2e link %s: %v", tmpE2eURL, err)
				}
				break
			}
		}
		e2eURL = gatherExtraURL
	}

	gcsMetricsURL := fmt.Sprintf("%s%s", e2eURL.String(), mustGatherPath)
	tempMetricsURL := strings.Replace(gcsMetricsURL, gcsPrefix+"/gcs", storagePrefix, -1)
	expectedMetricsURL, err := url.Parse(tempMetricsURL)
	if err != nil {
		return prowInfo, fmt.Errorf("failed to parse must-gather link %s: %v", tempMetricsURL, err)
	}
	prowInfo.MustGatherURL = expectedMetricsURL.String()
	return prowInfo, nil
}

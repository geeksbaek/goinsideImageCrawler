package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"flag"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/geeksbaek/goinside"
)

var (
	flagGall              = flag.String("gall", "", "http://m.dcinside.com/list.php?id=programming")
	defaultImageDirectory = "image"
	duration              = time.Second * 3

	history = struct {
		article map[string]bool
		image   map[string]bool
		mutex   *sync.Mutex
	}{
		article: map[string]bool{},
		image:   map[string]bool{},
		mutex:   new(sync.Mutex),
	}
)

func init() {
	os.Mkdir(defaultImageDirectory, 0700)
	root, err := filepath.Abs(filepath.Dir(os.Args[0]))
	if err != nil {
		panic(err)
	}
	filepath.Walk(root+"/"+defaultImageDirectory, func(path string, f os.FileInfo, err error) error {
		if checksum, err := fileToMD5(path); err == nil {
			history.mutex.Lock()
			defer history.mutex.Unlock()
			history.image[checksum] = true
		}
		return nil
	})
}

func main() {
	flag.Parse()
	if *flagGall == "" {
		log.Fatal("invalid args")
	}

	log.Printf("target is %s, crawl start.\n", *flagGall)

	// get first list from *flagGall
	ticker := time.Tick(duration)
	for {
		select {
		case <-ticker:
			log.Printf("goinside.GetList(%s, 1) called.\n", *flagGall)
			if list, err := goinside.GetList(*flagGall, 1); err == nil {
				go iterArticles(list.Articles)
			}
		}
	}
}

func iterArticles(articles []*goinside.Article) {
	for _, article := range articles {
		// if article has an image,
		if !article.HasImage {
			continue
		}

		// fetching the article,
		article, err := goinside.GetArticle(article.URL)
		if err != nil {
			continue
		}
		// and passing the images to process()
		for _, image := range article.Images {
			go func(URL string) {
				log.Printf("#%v article has an image. process start.\n", article.Number)
				if err := process(URL, article.Number); err != nil {
					log.Printf("#%v article process failed. %v", article.Number, err)
				} else {
					log.Printf("#%v article process succeed.", article.Number)
				}
			}(image)
		}
	}
}

func process(URL, articleNumber string) error {
	// first, fetching image
	resp, err := fetchImage(URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// second, hashing for check duplicate
	checksum := bytesToMD5(body)

	// get filename,
	filename, err := getFilename(resp)
	if err != nil {
		return err
	}

	history.mutex.Lock()
	defer history.mutex.Unlock()

	switch {
	// if you already seen this article, return error
	case history.article[articleNumber] != false:
		return errors.New("this article already seen")
	// if the image do not duplicated, return error
	case history.image[checksum] != false:
		return errors.New("it's a duplicated image")
	}

	// save it.
	if err := saveImage(body, filename); err != nil {
		return err
	}
	history.article[articleNumber] = true
	history.image[checksum] = true
	return nil
}

func fetchImage(URL string) (*http.Response, error) {
	form := func(m map[string]string) io.Reader {
		data := url.Values{}
		for k, v := range m {
			data.Set(k, v)
		}
		return strings.NewReader(data.Encode())
	}

	idRe := regexp.MustCompile(`id=([^&]*)`)
	idMatched := idRe.FindStringSubmatch(URL)
	if len(idMatched) != 2 {
		return nil, errors.New("cannot find id from url")
	}

	noRe := regexp.MustCompile(`no=([^&]*)`)
	noMatched := noRe.FindStringSubmatch(URL)
	if len(noMatched) != 2 {
		return nil, errors.New("cannot find no from url")
	}

	req, err := http.NewRequest("GET", URL, form(map[string]string{
		"id": idMatched[1],
		"no": noMatched[1],
	}))
	if err != nil {
		return nil, err
	}
	client := &http.Client{}
	return client.Do(req)
}

func bytesToMD5(body []byte) string {
	hasher := md5.New()
	hasher.Write(body)
	return hex.EncodeToString(hasher.Sum(nil))
}

func fileToMD5(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := md5.New()
	_, err = io.Copy(hasher, file)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func getFilename(resp *http.Response) (string, error) {
	filenameRe := regexp.MustCompile(`filename=(.*)`)
	contentDisposition := resp.Header.Get("Content-Disposition")
	matched := filenameRe.FindStringSubmatch(contentDisposition)
	if len(matched) != 2 {
		return "", errors.New("cannot find filename from content-position")
	}
	return matched[1], nil
}

func saveImage(body []byte, path string) error {
	file, err := os.Create(defaultImageDirectory + "/" + path)
	if err != nil {
		return err
	}
	_, err = io.Copy(file, bytes.NewReader(body))
	if err != nil {
		return err
	}
	file.Close()
	return nil
}

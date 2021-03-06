package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/geeksbaek/goinside"
	"github.com/sirupsen/logrus"
)

type mutexMap struct {
	storage map[string]bool
	mutex   *sync.RWMutex
}

func (m *mutexMap) set(key string, value bool) {
	m.mutex.Lock()
	defer m.mutex.Unlock()
	m.storage[key] = value
}

func (m *mutexMap) get(key string) bool {
	m.mutex.RLock()
	defer m.mutex.RUnlock()
	return m.storage[key]
}

// flags
var (
	flagURL    = flag.String("url", "", "http://m.dcinside.com/list.php?id=programming")
	flagGallID = flag.String("gall", "", "programming")
)

var (
	defaultImageDirectory = "./images"
	imageSubdirectory     = ""
	duration              = time.Second * 5

	history = struct {
		article *mutexMap
		image   *mutexMap
	}{
		article: &mutexMap{map[string]bool{}, new(sync.RWMutex)},
		image:   &mutexMap{map[string]bool{}, new(sync.RWMutex)},
	}

	errDuplicateImage      = errors.New("duplicated image")
	errInvalidArgs         = errors.New("invalid args")
	errCannotFoundID       = errors.New("cannot found id from url")
	errCannotFoundNo       = errors.New("cannot found no from url")
	errCannotFoundFilename = errors.New("cannot found filename from content-position")

	idRe = regexp.MustCompile(`id=([^&]*)`)
	noRe = regexp.MustCompile(`no=([^&]*)`)
)

func main() {
	flag.Parse()
	URL, gallID := getID(*flagURL, *flagGallID)

	imageSubdirectory = fmt.Sprintf(`%s/%s`, defaultImageDirectory, gallID)
	mkdir(imageSubdirectory)
	hashingExistImages(imageSubdirectory)

	logrus.Infof("target is %s, crawl start.", gallID)
	// get first list of *flagGall every tick.
	// and iterate all articles.
	ticker := time.Tick(duration)
	for range ticker {
		logrus.Infof("Fetching First Page of %v...", gallID)
		if list, err := goinside.FetchList(gallID, 1); err != nil {
			logrus.Errorf("%v: %v", URL, err)
		} else {
			go iterate(list.Items)
		}
	}
}

func getID(URL, gallID string) (retURL, retGallID string) {
	switch {
	case URL != "" && gallID == "":
		matched := idRe.FindStringSubmatch(URL)
		if len(matched) == 2 {
			retURL = URL
			retGallID = matched[1]
			return
		}
	case URL == "" && gallID != "":
		retURL = fmt.Sprintf("http://m.dcinside.com/list.php?id=%v", gallID)
		retGallID = gallID
		return
	}
	panic(errInvalidArgs)
}

func mkdir(path string) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		err := os.MkdirAll(path, 0700)
		if err != nil {
			panic(err)
		}
		return
	}
}

func hashingExistImages(path string) {
	fileRenameToHash := func(path, extension string) (err error) {
		newpath, err := hashingFile(path)
		if err != nil {
			return
		}
		newpath = fmt.Sprintf(`%s/%s`, path, newpath)
		newfilename := strings.Join([]string{newpath, extension}, ".")
		err = os.Rename(path, newfilename)
		if err != nil {
			return
		}
		return
	}
	forEachImages := func(path string, f os.FileInfo, _ error) (err error) {
		if f.IsDir() {
			return
		}
		filename, extension := splitPath(f.Name())
		// check filename is not hash.
		// if not, hashing and rename.
		if len(filename) != 40 {
			fileRenameToHash(path, extension)
		}
		history.image.set(filename, true)
		return
	}
	filepath.Walk(path, forEachImages)
}

// if find an image included article, fetching it.
func iterate(articles []*goinside.ListItem) {
	for _, article := range articles {
		if article.HasImage {
			go fetchArticle(article)
		}
	}
}

func fetchArticle(item *goinside.ListItem) {
	imageURLs, err := item.FetchImageURLs()
	if err != nil {
		return
	}
	// if you already seen this article, return.
	if history.article.get(item.Number) == true {
		return
	}
	// if not, passing the images to process()
	imageCount := len(imageURLs)
	successAll := true
	wg := new(sync.WaitGroup)
	wg.Add(len(imageURLs))
	for i, imageURL := range imageURLs {
		i, imageURL := i, imageURL
		go func() {
			defer wg.Done()
			switch process(imageURL) {
			case errDuplicateImage:
				logrus.Infof("%v (%v/%v) Dup.", item.Subject, i+1, imageCount)
			case nil:
				logrus.Infof("%v (%v/%v) OK.", item.Subject, i+1, imageCount)
			default:
				logrus.Infof("%v (%v/%v) Failed. %v", item.Subject, i+1, imageCount, err)
				successAll = false
			}
		}()

	}
	wg.Wait()
	if successAll {
		history.article.set(item.Number, true)
	}
}

// process will fetching the image, and hashing,
// and comparing the history with it.
// if it already exists, return errDuplicateImage.
// if not, save it, and add to the history.
func process(URL goinside.ImageURLType) (err error) {
	image, filename, err := URL.Fetch()
	if err != nil {
		return
	}
	hash := hashingBytes(image)
	if history.image.get(hash) == true {
		err = errDuplicateImage
		return
	}
	_, extension := splitPath(filename)
	filename = strings.Join([]string{hash, extension}, ".")
	path := fmt.Sprintf(`%s/%s`, imageSubdirectory, filename)
	err = saveImage(image, path)
	if err != nil {
		return
	}
	history.image.set(hash, true)
	return
}

func formMaker(m map[string]string) (reader io.Reader) {
	data := url.Values{}
	for k, v := range m {
		data.Set(k, v)
	}
	reader = strings.NewReader(data.Encode())
	return
}

func hashingBytes(data []byte) (hash string) {
	hasher := sha1.New()
	hasher.Write(data)
	hash = hex.EncodeToString(hasher.Sum(nil))
	return
}

func hashingFile(path string) (hash string, er error) {
	data, err := ioutil.ReadFile(path)
	if err != nil {
		return
	}
	hash = hashingBytes(data)
	return
}

func getFilename(resp *http.Response) (filename string, err error) {
	filenameRe := regexp.MustCompile(`filename=(.*)`)
	contentDisposition := resp.Header.Get("Content-Disposition")
	matched := filenameRe.FindStringSubmatch(contentDisposition)
	if len(matched) != 2 {
		err = errCannotFoundFilename
		return
	}
	filename = strings.ToLower(matched[1])
	return
}

func saveImage(data []byte, path string) (err error) {
	file, err := os.Create(path)
	if err != nil {
		return
	}
	_, err = io.Copy(file, bytes.NewReader(data))
	if err != nil {
		return
	}
	file.Close()
	return
}

func splitPath(fullname string) (filename, extension string) {
	splitedName := strings.Split(fullname, ".")
	filename = strings.Join(splitedName[:len(splitedName)-1], ".")
	extension = splitedName[len(splitedName)-1]
	return
}

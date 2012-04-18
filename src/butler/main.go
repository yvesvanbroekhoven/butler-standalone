package main

import "sync"
import "flag"
import "net/http"
import "net/url"
import "io/ioutil"
import "fmt"
import "html"
import "regexp"
import "strings"
import "path/filepath"
import "os"
import "encoding/json"

import pqueue "github.com/nu7hatch/gopqueue"

type Crawler struct {
  www       bool
  domains   map[string]bool
  queue   * pqueue.Queue
  waiter    sync.WaitGroup
  known     map[string]bool

  report_dir string
  reporters  []Reporter
}

type task struct {
  url * url.URL
}

func (t * task) Less (other interface{}) (bool) {
  return (len(t.url.String()) < len(other.(*task).url.String()))
}

func New(report_dir string)(c * Crawler, err error){
  report_dir, err = filepath.Abs(report_dir)
  if err != nil { return }

  report_dir = filepath.Clean(report_dir)

  c = &Crawler{
    report_dir : report_dir,
    domains    : make(map[string]bool),
    queue      : pqueue.New(0),
    known      : make(map[string]bool),
    reporters  : make([]Reporter, 0),
  }
  return
}

func (c * Crawler) RegisterReporter (reporter Reporter) {
  c.reporters = append(c.reporters, reporter)
}

func (c * Crawler) report_success (u * url.URL, status uint) {
  for _, reporter := range c.reporters {
    reporter.Success(u, status)
  }
}

func (c * Crawler) report_ignored (u * url.URL, status uint, reason interface{}) {
  for _, reporter := range c.reporters {
    reporter.Ignored(u, status, reason)
  }
}

func (c * Crawler) report_error (u * url.URL, status uint, reason interface{}) {
  for _, reporter := range c.reporters {
    reporter.Error(u, status, reason)
  }
}

func (c * Crawler) allow (domain string) {
  c.domains[domain] = true
}

func (c * Crawler) enqueue (link * url.URL, base * url.URL) {
  if base != nil {
    link = base.ResolveReference(link)
    link.Fragment = ""
  }

  if link.Path == "" {
    link.Path = "/"
  }

  link.Fragment = ""

  if link.Host != "" {
    link.Host = c.normalize_host(link.Host)
  }

  if _, present := c.known[link.String()]; present {
    return
  }

  c.known[link.String()] = true

  if link.Scheme == "http" {
    if c.domains[link.Host] {
      c.waiter.Add(1)
      c.queue.Enqueue(&task{url: link})
      return
    } else {
      c.report_ignored(link, 0, "external domain")
      return
    }
  } else {
    c.report_ignored(link, 0, "wrong scheme: "+link.Scheme)
    return
  }

  /*c.waiter.Add(1)*/
  /*c.queue <- u.String()*/
}

func (c * Crawler) Run (pool_size int) {
  os.RemoveAll(c.report_dir)
  os.MkdirAll(c.report_dir, 0755)

  for _, reporter := range c.reporters {
    reporter.Start()
  }

  for i := 0; i <= pool_size; i ++ {
    go func(){
      for{
        t := c.queue.Dequeue()
        c.process_url(t.(*task).url)
        c.waiter.Done()
      }
    }()
  }

  c.waiter.Wait()

  for _, reporter := range c.reporters {
    reporter.Finish(c.report_dir)
  }
}

var pattern * regexp.Regexp

func (c * Crawler) process_url (page * url.URL) {
  resp, err := http.Get(page.String())
  if err != nil {
    c.report_error(page, 0, err)
    return
  }

  defer resp.Body.Close()
  body, err := ioutil.ReadAll(resp.Body)

  // check for redirects

  if resp.StatusCode != 200 {
    c.report_error(page, uint(resp.StatusCode), nil)
    return
  }

  if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
    c.report_ignored(page, 0, fmt.Sprintf("content-type: %v", resp.Header.Get("Content-Type")))
    return
  }

  links := pattern.FindAllStringSubmatch(string(body), -1)
  for _, m := range links {
    link := m[1]

    link = html.UnescapeString(link)

    if strings.HasPrefix(link, "#") {
      continue
    }

    u, err := url.Parse(link)
    if err != nil { fmt.Printf("Invalid url: %s\n", link); continue }

    c.enqueue(u, page)
  }

  c.report_success(page, uint(resp.StatusCode))
}

func (c * Crawler) normalize_host (host string) (string) {
  if strings.HasPrefix(host, "www.") {
    if c.www {
      return host
    } else {
      return host[4:]
    }
  } else {
    if c.www {
      return "www." + host
    } else {
      return host
    }
  }
  return ""
}

func (c * Crawler) Load (path string) (err error) {
  var config   Config
  var u      * url.URL

  jsonBlob, err := ioutil.ReadFile(path)
  if err != nil { return }

  err = json.Unmarshal(jsonBlob, &config)
  if err != nil { return }

  c.www = config.Www

  for _, domain := range config.Domains {
    domain = c.normalize_host(domain)
    c.allow(domain)

    u, err = url.Parse("http://"+domain+"/")
    if err != nil { return }

    c.enqueue(u, nil)
  }

  return
}

type Config struct {
  Www     bool     `json:"www"`
  Domains []string `json:"domains"`
}

func init() {
  var err error
  pattern, err = regexp.Compile("[<]a[^>]+href[=][\"']([^\"']+)[\"']")
  if err != nil { panic(err) }
}

var config_file = flag.String("config", "config.json", "The path to the config file.")
var report_dir  = flag.String("report", "report",      "The path to the report directory.")

func main() {
  flag.Parse()

  c, err := New(*report_dir)
  if err != nil { panic(err) }
  c.RegisterReporter(new(SitemapReporter))
  c.RegisterReporter(new(StdoutReporter))
  c.RegisterReporter(new(ErrorReporter))
  c.RegisterReporter(new(IgnoreReporter))

  err = c.Load(*config_file)
  if err != nil { panic(err) }
  c.Run(2)
}

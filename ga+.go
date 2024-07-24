package main

import (
    "bufio"
    "crypto/tls"
    "flag"
    "fmt"
    "net"
    "os"
    "strconv"
    "strings"
    "time"
)

func printUsage() {
    fmt.Fprintf(os.Stderr, "Usage of %s:\n\n", os.Args[0])
    flag.PrintDefaults()
}

func dialNNTP(server string, port int, useTLS bool) (net.Conn, error) {
    address := fmt.Sprintf("%s:%d", server, port)
    if useTLS {
        return tls.Dial("tcp", address, &tls.Config{
            InsecureSkipVerify: true, // Hinweis: In Produktionsumgebungen sollten Sie dies nicht verwenden
        })
    }
    return net.Dial("tcp", address)
}

func authenticateNNTP(conn net.Conn, username, password string) error {
    fmt.Fprintf(conn, "AUTHINFO USER %s\r\n", username)
    reader := bufio.NewReader(conn)
    response, err := reader.ReadString('\n')
    if err != nil {
        return err
    }
    if !strings.HasPrefix(response, "381") {
        return fmt.Errorf("unexpected response to USER: %s", response)
    }

    fmt.Fprintf(conn, "AUTHINFO PASS %s\r\n", password)
    response, err = reader.ReadString('\n')
    if err != nil {
        return err
    }
    if !strings.HasPrefix(response, "281") {
        return fmt.Errorf("authentication failed: %s", response)
    }
    return nil
}

func getGroups(server string, port int, pattern string, username, password string, useTLS bool) ([]string, error) {
    conn, err := dialNNTP(server, port, useTLS)
    if err != nil {
        return nil, err
    }
    defer conn.Close()

    if username != "" && password != "" {
        err = authenticateNNTP(conn, username, password)
        if err != nil {
            return nil, err
        }
    }

    fmt.Fprintf(conn, "list active %s\r\n", pattern)
    reader := bufio.NewReader(conn)
    var groups []string
    for {
        line, err := reader.ReadString('\n')
        if err != nil || line == ".\r\n" {
            break
        }
        fields := strings.Fields(line)
        if len(fields) > 0 {
            groups = append(groups, fields[0])
        }
    }
    return groups, nil
}

func normalizeSearchTerm(term string) (string, string) {
    term = strings.ToLower(strings.TrimSpace(term))
    parts := strings.SplitN(term, "<", 2)
    name := strings.TrimSpace(parts[0])
    email := ""
    if len(parts) > 1 {
        email = strings.Trim(parts[1], ">")
    }
    return name, email
}

func getArticleNumbers(reader *bufio.Reader, poster string, days int, exactMatch bool) []string {
    cutoffDate := time.Now().AddDate(0, 0, -days)
    var articleNumbers []string

    searchName, searchEmail := normalizeSearchTerm(poster)

    for {
        line, err := reader.ReadString('\n')
        if err != nil || line == ".\r\n" {
            break
        }

        fields := strings.Split(line, "\t")
        if len(fields) < 4 {
            continue
        }

        fromField := fields[2]
        name, email := normalizeSearchTerm(fromField)

        var match bool
        if exactMatch {
            match = (name == searchName && email == searchEmail)
        } else {
            match = strings.Contains(name, searchName) && strings.Contains(email, searchEmail)
        }

        if match {
            dateStr := strings.Trim(fields[3], " ")
            dateStr = strings.Split(dateStr, " (")[0] // Remove the (CEST) part
            date, err := time.Parse("Mon, 2 Jan 2006 15:04:05 -0700", dateStr)
            if err == nil && (days == 0 || date.After(cutoffDate)) {
                articleNumbers = append(articleNumbers, fields[0])
            }
        }
    }

    return articleNumbers
}

func getArticles(server string, port int, groups []string, poster string, days int, exactMatch bool, username, password string, useTLS bool) ([]string, error) {
    conn, err := dialNNTP(server, port, useTLS)
    if err != nil {
        return nil, err
    }
    defer conn.Close()

    reader := bufio.NewReader(conn)
    _, err = reader.ReadString('\n')
    if err != nil {
        return nil, err
    }

    if username != "" && password != "" {
        err = authenticateNNTP(conn, username, password)
        if err != nil {
            return nil, err
        }
    }

    var matchedArticles []string
    for _, group := range groups {
        fmt.Fprintf(conn, "group %s\r\n", group)
        groupResponse, err := reader.ReadString('\n')
        if err != nil {
            return nil, err
        }

        if !strings.HasPrefix(groupResponse, "211 ") {
            continue
        }

        parts := strings.Fields(groupResponse)
        if len(parts) < 4 {
            return nil, fmt.Errorf("unexpected group response: %s", groupResponse)
        }
        firstArticle, _ := strconv.Atoi(parts[2])
        lastArticle, _ := strconv.Atoi(parts[3])

        fmt.Fprintf(conn, "xover %d-%d\r\n", firstArticle, lastArticle)

        articleNumbers := getArticleNumbers(reader, poster, days, exactMatch)

        for _, articleNum := range articleNumbers {
            fmt.Fprintf(conn, "article %s\r\n", articleNum)
            var articleContent strings.Builder
            articleContent.WriteString(fmt.Sprintf("--- Article %s:%s ---\r\n", group, articleNum))
            for {
                line, err := reader.ReadBytes('\n')
                if err != nil {
                    return nil, err
                }
                if string(line) == ".\r\n" {
                    break
                }
                articleContent.Write(line)
            }
            matchedArticles = append(matchedArticles, articleContent.String())
        }
    }

    fmt.Fprintf(conn, "quit\r\n")

    return matchedArticles, nil
}

func main() {
    server := flag.String("server", "news.i2pn2.org", "NNTP server address")
    port := flag.Int("port", 119, "NNTP server port")
    group := flag.String("group", "", "Newsgroup or pattern to search")
    poster := flag.String("poster", "", "Poster to search for")
    days := flag.Int("days", 0, "Number of days to search back (0 for all)")
    exactMatch := flag.Bool("exact", false, "Use exact matching for poster name")
    username := flag.String("username", "", "NNTP username")
    password := flag.String("password", "", "NNTP password")
    useTLS := flag.Bool("tls", false, "Use TLS connection")

    flag.Parse()

    if *poster == "" {
        fmt.Println("No parameter provided\n")
        printUsage()
        return
    }

    var groups []string
    var err error
    if *group != "" {
        if strings.Contains(*group, "*") {
            groups, err = getGroups(*server, *port, *group, *username, *password, *useTLS)
            if err != nil {
                fmt.Printf("Error getting groups: %v\n", err)
                return
            }
        } else {
            groups = []string{*group}
        }
    } else {
        groups, err = getGroups(*server, *port, "", *username, *password, *useTLS)
        if err != nil {
            fmt.Printf("Error getting groups: %v\n", err)
            return
        }
    }

    articles, err := getArticles(*server, *port, groups, *poster, *days, *exactMatch, *username, *password, *useTLS)
    if err != nil {
        fmt.Printf("Error: %v\n", err)
        return
    }

    fmt.Printf("Found %d articles by %s:\n", len(articles), *poster)
    for _, article := range articles {
        fmt.Print(article)
        fmt.Print(".\n")
    }
}

# Parsemail - 简洁的 Go 邮件解析库

[![Build Status](https://circleci.com/gh/DusanKasan/parsemail.svg?style=shield&circle-token=:circle-token)](https://circleci.com/gh/DusanKasan/parsemail) [![Coverage Status](https://coveralls.io/repos/github/DusanKasan/Parsemail/badge.svg?branch=master)](https://coveralls.io/github/DusanKasan/Parsemail?branch=master) [![Go Report Card](https://goreportcard.com/badge/github.com/DusanKasan/parsemail)](https://goreportcard.com/report/github.com/DusanKasan/parsemail)

本库可以将邮件解析为比 `net/mail` 更便于使用的结构。除了 RFC 5322 标头，Parsemail 还会以已解码的数据流及其元数据的形式提供 HTML 和纯文本正文、附件与内嵌内容。

MIME 实体会递归解析，包括任意嵌套的 multipart 子类型。本库支持解码 Base64 和 quoted-printable 传输编码、将常见旧式字符集转换为 UTF-8、解码 RFC 2047 标头，并支持 RFC 2231 文件名。

## 基本用法

使用 `io.Reader` 读取原始邮件并进行解析。返回的 `Email` 通过公开字段提供标准邮件标头和已解码的内容。

```go
var reader io.Reader // 用于读取邮件内容
email, err := parsemail.Parse(reader) // 返回 Email 结构体和错误
if err != nil {
    // 处理错误
}

fmt.Println(email.Subject)
fmt.Println(email.From)
fmt.Println(email.To)
fmt.Println(email.HTMLBody)
```

## 获取附件

附件以 `Attachment` 值的形式提供，其中包含媒体类型、文件名和已解码的数据流。

```go
var reader io.Reader
email, err := parsemail.Parse(reader)
if err != nil {
    // 处理错误
}

for _, a := range(email.Attachments) {
    fmt.Println(a.Filename)
    fmt.Println(a.ContentType)
    // 读取 a.Data 中的数据
}
```

## 获取内嵌文件

内嵌文件的获取方式与附件相同。它们包含媒体类型、已解码的数据流，以及用于在邮件正文中引用该文件的内容 ID。

```go
var reader io.Reader
email, err := parsemail.Parse(reader)
if err != nil {
    // 处理错误
}

for _, a := range(email.EmbeddedFiles) {
    fmt.Println(a.CID)
    fmt.Println(a.ContentType)
    // 读取 a.Data 中的数据
}
```

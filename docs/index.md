# What is Tijori?

Tijori is a small program that stores files for your apps. It speaks the
**S3 API**, the same language as Amazon S3, so any app or library that can
use S3 can use Tijori instead. It runs on your own computer or server.

![How Tijori fits in](images/diagram.svg)

- Your **apps** upload and download files using normal S3 libraries.
- **Tijori** checks keys, enforces quotas, resizes images and makes video thumbnails.
- Files are stored as **plain files** on disk, so you can see and copy them.
- **Backups** copy everything to another folder, an SSH server or a cloud bucket.

## Why use it?

- **Simple.** One file to download. No database to set up.
- **Cheap.** Use the disk you already pay for.
- **Private.** Your files stay on your server.
- **Compatible.** Laravel, Node.js, Python, Go, Flutter and React Native all work.

## When not to use it

Tijori runs on one server. It does not copy data between servers by itself,
and it has no file versioning or complex permission policies. For a cluster
of servers, look at Garage or SeaweedFS.

## Words you will see

| Word | Meaning |
| --- | --- |
| **Bucket** | A top-level folder, for example `photos` or `invoices`. |
| **Object** | A file inside a bucket. Its name is called the **key**, for example `2024/beach.jpg`. |
| **Access key / secret key** | A user name and password for apps. Each app should get its own. |
| **Endpoint** | The address of your Tijori, for example `https://files.example.com`. |
| **Presigned URL** | A link that works for a limited time without sharing your secret. |

Next: [Quick Start in 5 minutes](quick-start.md).

!!! tip "Using an AI assistant?"
    Give it [llms-full.txt](llms-full.txt): the whole manual in one plain-text file, written so
    ChatGPT, Claude, Cursor and similar tools answer questions about Tijori correctly.
    [llms.txt](llms.txt) is the short index in the [llms.txt](https://llmstxt.org) format.

# Matcha RSS Digest and Reader in Docker

This is a single docker container that includes [Matcha](https://github.com/piqoni/matcha) which is used to generate a daily digest of RSS feeds in markdown format. Packaged in with this is a simple webapp to read the markdown files.

The web app has a similar goal as [go-digest](https://github.com/piqoni/go-digest). However that project is geared toward building a web site for github pages. I wanted to host a web app from a self-hosted container, which will only be available on my local network.

## Instructions

Docker compose:

```

```



## Notes

- Thanks to [Edi Piqoni](https://piqoni.github.io/) for creating Matcha
- This repo was developed with LLM help. I probably won't be adding much as far as features, since its currently doing what I need, and I don't understand enough go or css to keep track of anything more complicated.
# Contributing

To build and install a development version of Drafter locally, first make sure that you have all the non-Drafter dependencies installed (see [installation](./README.md#installation)), then run the following:

```shell
$ git clone https://github.com/loopholelabs/drafter.git
$ cd drafter
$ make depend
$ sudo make install
```

To create VM packages, start or live migrate them, see the [tutorial](./README.md#tutorial).

Drafter uses GitHub to manage reviews of pull requests.

- If you have a trivial fix or improvement, go ahead and create a pull request,
  addressing (with `@...`) the maintainer of this repository (see
  [MAINTAINERS.md](./MAINTAINERS.md)) in the description of the pull request.

- If you plan to do something more involved, first discuss your ideas
  on our [Discord](https://loopholelabs.io/discord).
  This will avoid unnecessary work and surely give you and us a good deal
  of inspiration.

- Relevant coding style guidelines are the [Go Code Review
  Comments](https://code.google.com/p/go-wiki/wiki/CodeReviewComments)
  and the _Formatting and style_ section of Peter Bourgon's [Go: Best
  Practices for Production
  Environments](http://peter.bourgon.org/go-in-production/#formatting-and-style).

- Be sure to sign off on the [CLA](./CLA.md). Once you submit your pull request, [CLA Assistant](https://github.com/contributor-assistant/github-action) will ask you sign off before your pull request can be merged.

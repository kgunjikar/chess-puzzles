# Stockfish release tag to build with `make stockfish` (override: make stockfish STOCKFISH_TAG=sf_17).
STOCKFISH_TAG ?= sf_17.1
STOCKFISH_REPO ?= https://github.com/official-stockfish/Stockfish.git
SF_SRC := third_party/Stockfish

.PHONY: build test vet ui run stockfish clean

build:
	go build -o bin/ ./cmd/...

vet:
	go vet ./...

test:
	go test ./...

ui: build
	./bin/mate-puzzles-ui

# Run the generator UI with the sample puzzles preloaded.
run: build
	./bin/mate-puzzles-ui -load examples/sample_puzzles.jsonl $(ARGS)

# Fetch and build a pinned Stockfish into bin/stockfish (found automatically by both commands).
stockfish: bin/stockfish

bin/stockfish:
	@mkdir -p bin third_party
	@if [ ! -d $(SF_SRC) ]; then git clone --depth 1 --branch $(STOCKFISH_TAG) $(STOCKFISH_REPO) $(SF_SRC); fi
	$(MAKE) -C $(SF_SRC)/src -j build
	cp $(SF_SRC)/src/stockfish bin/stockfish

clean:
	rm -rf bin

# ---------- Static site ----------
SFJS_VERSION ?= 19.0.1
SFJS_SHA256 ?= c3695e2a3afbc61c6439c689cb8b95cec4627bcda407193f6b7c3a5702fac839
SFJS_DIR := third_party/stockfish-js
BANK ?= puzzles/bank.jsonl
N ?= 50

.PHONY: engine-wasm bank site serve-site deploy deploy-firebase

# Download the stockfish.js "lite, single-threaded" WASM build (pinned + checksummed).
engine-wasm: $(SFJS_DIR)/stockfish-19-lite-single.wasm

$(SFJS_DIR)/stockfish-19-lite-single.wasm:
	@mkdir -p $(SFJS_DIR)
	curl -fsSL https://registry.npmjs.org/stockfish/-/stockfish-$(SFJS_VERSION).tgz -o $(SFJS_DIR)/pkg.tgz
	echo "$(SFJS_SHA256)  $(SFJS_DIR)/pkg.tgz" | shasum -a 256 -c -
	tar -xzf $(SFJS_DIR)/pkg.tgz -C $(SFJS_DIR) --strip-components=2 package/bin/stockfish-19-lite-single.js package/bin/stockfish-19-lite-single.wasm
	tar -xzf $(SFJS_DIR)/pkg.tgz -C $(SFJS_DIR) --strip-components=1 package/Copying.txt
	rm $(SFJS_DIR)/pkg.tgz

# Grow the puzzle bank: N more puzzles of each length (appends; keep it append-only).
bank: build
	./bin/mate-puzzles -mates 1-5 -n $(N) -threads 4 -out $(BANK)

site: build engine-wasm
	./bin/build-site -bank $(BANK) -config site.json -wasm $(SFJS_DIR) -out dist

serve-site: build engine-wasm
	./bin/build-site -bank $(BANK) -config site.json -wasm $(SFJS_DIR) -out dist -serve localhost:8080

deploy-firebase: site
	firebase deploy --only hosting

# Build and deploy dist/ to the Cloudflare Worker in wrangler.jsonc
# (chesspuzzlesfree.com). Run `npx wrangler login` once first.
deploy: site
	npx --yes wrangler@4 deploy

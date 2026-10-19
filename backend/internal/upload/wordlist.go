package upload

// Curated word lists for the "gfycat" slug strategy (adjective-adjective-animal
// plus a random suffix, e.g. "brave-quick-otter-k3m9").
//
// Curation intent:
//   - Every entry is lowercase ASCII [a-z] only: no digits, spaces, hyphens,
//     accents or emoji. This keeps generated slugs within the ASCII slug
//     invariant so they shard correctly (byte-based shard()), stay URL-safe and
//     never break the "/raw/{slug}.ext" embedding (no dots).
//   - slugAdjectives holds only positive or neutral descriptors. Nothing
//     negative, insulting, political or crude is included, so no random
//     adjective+animal pairing can read as an offensive phrase (e.g. no "fat",
//     "dumb", "lazy", "ugly", "dirty").
//   - slugAnimals holds only neutral, inoffensive creatures. Animals whose names
//     are slurs, insults or carry loaded connotations are deliberately excluded
//     (e.g. no rat, pig, snake, weasel, ape/monkey), again so every combination
//     stays harmless.
//
// Both lists are intentionally ~200 entries each, giving ~200*200*200 ≈ 8M
// distinct word combinations — far too few to keep links unguessable on their
// own, hence the random suffix (see gfycatSlug).

// slugAdjectives are positive/neutral, lowercase-ASCII descriptors.
var slugAdjectives = []string{
	"able", "agile", "airy", "amber", "ample", "artful", "autumn", "balmy",
	"blithe", "bold", "bonny", "brave", "breezy", "bright", "brisk", "bubbly",
	"buoyant", "calm", "canny", "charming", "cheerful", "cheery", "chipper",
	"chirpy", "classy", "clean", "clear", "clever", "cobalt", "comfy", "cool",
	"cosmic", "cozy", "crafty", "crimson", "crisp", "cuddly", "curious", "dainty",
	"dandy", "dapper", "daring", "dashing", "dazzling", "deft", "dreamy", "eager",
	"earnest", "easy", "elegant", "ember", "epic", "fair", "faithful", "fancy",
	"fearless", "feisty", "fiery", "fine", "fleet", "floral", "fluffy", "fond",
	"frank", "free", "fresh", "friendly", "frosty", "funny", "fuzzy", "gallant",
	"gentle", "giddy", "gifted", "glad", "gleaming", "glowing", "golden", "good",
	"graceful", "grand", "grateful", "great", "groovy", "handy", "happy", "hardy",
	"hearty", "helpful", "honest", "hopeful", "humble", "jaunty", "jazzy", "jolly",
	"jovial", "joyful", "keen", "kind", "kindly", "lively", "loyal", "lucky",
	"lunar", "lush", "magic", "mellow", "merry", "mighty", "mild", "minty",
	"misty", "modest", "neat", "nifty", "nimble", "noble", "olive", "peppy",
	"perky", "placid", "playful", "plucky", "plush", "polished", "polite", "posh",
	"prime", "prompt", "proper", "proud", "quaint", "quick", "quiet", "quirky",
	"radiant", "rapid", "rare", "ready", "regal", "robust", "rosy", "royal",
	"rustic", "sage", "sandy", "sassy", "savvy", "scarlet", "serene", "sharp",
	"shiny", "silky", "silver", "sincere", "sleek", "smart", "smooth", "snappy",
	"snug", "soft", "solar", "solid", "sparkly", "speedy", "spicy", "spiffy",
	"spirited", "splendid", "spry", "stellar", "sturdy", "suave", "sunny", "super",
	"superb", "swanky", "sweet", "swift", "teal", "tender", "thankful", "tidy",
	"timely", "tiny", "trim", "true", "trusty", "upbeat", "urbane", "valiant",
	"velvet", "vibrant", "vivid", "warm", "whimsical", "willing", "wise", "witty",
	"worthy", "zany", "zealous", "zesty", "zippy",
}

// slugAnimals are neutral, inoffensive, lowercase-ASCII animal names.
var slugAnimals = []string{
	"otter", "panda", "koala", "fox", "owl", "wolf", "bear", "hare", "rabbit",
	"deer", "elk", "moose", "bison", "buffalo", "seal", "walrus", "whale",
	"dolphin", "orca", "narwhal", "manatee", "badger", "beaver", "bobcat", "lynx",
	"cougar", "puma", "jaguar", "leopard", "cheetah", "lion", "tiger", "panther",
	"ocelot", "serval", "caracal", "cat", "kitten", "dog", "puppy", "hound",
	"beagle", "corgi", "husky", "terrier", "spaniel", "poodle", "collie",
	"pointer", "setter", "dingo", "coyote", "fennec", "chipmunk", "squirrel",
	"marmot", "hamster", "gerbil", "hedgehog", "mole", "ferret", "mink", "marten",
	"sable", "stoat", "wombat", "wallaby", "kangaroo", "quokka", "platypus",
	"echidna", "armadillo", "sloth", "anteater", "tapir", "capybara", "porcupine",
	"yak", "ibex", "gazelle", "antelope", "impala", "springbok", "oryx", "kudu",
	"eland", "zebra", "giraffe", "okapi", "camel", "llama", "alpaca", "vicuna",
	"guanaco", "donkey", "pony", "foal", "colt", "mare", "mustang", "bronco",
	"goat", "lamb", "ram", "calf", "hawk", "falcon", "eagle", "osprey", "kestrel",
	"harrier", "kite", "condor", "heron", "egret", "crane", "stork", "ibis",
	"flamingo", "pelican", "swan", "goose", "mallard", "grebe", "loon", "puffin",
	"tern", "gull", "petrel", "albatross", "gannet", "cormorant", "robin",
	"sparrow", "finch", "canary", "wren", "lark", "thrush", "warbler", "oriole",
	"tanager", "cardinal", "bluebird", "jay", "magpie", "raven", "starling",
	"swallow", "martin", "chickadee", "nuthatch", "woodpecker", "kingfisher",
	"hummingbird", "sunbird", "parrot", "parakeet", "macaw", "cockatoo", "budgie",
	"dove", "quail", "partridge", "pheasant", "grouse", "ptarmigan", "peacock",
	"roadrunner", "toucan", "hornbill", "kiwi", "emu", "ostrich", "rhea",
	"cassowary", "penguin", "gecko", "skink", "iguana", "chameleon", "anole",
	"tortoise", "turtle", "terrapin", "newt", "salamander", "frog", "treefrog",
	"koi", "guppy", "minnow", "trout", "salmon", "tuna", "marlin", "seahorse",
	"starfish", "octopus", "nautilus", "crab", "lobster", "shrimp", "prawn",
	"snail", "cricket", "ladybug", "firefly", "dragonfly", "butterfly", "mantis",
	"cicada",
}

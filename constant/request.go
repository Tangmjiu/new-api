package constant

import "math"

// MaxRelayOutputTokens is shared by protocol validation and quota-derived caps.
const MaxRelayOutputTokens = math.MaxInt32 / 2

package currency

import "errors"

/*
ErrRateUnavailable is returned when the exchange rate cannot be obtained.
This happens when:
  - The live API is unreachable or returns a non-200 status, AND
  - The in-memory cache is either empty or older than 24 hours.

The API layer maps this to HTTP 503 Service Unavailable.
*/
var ErrRateUnavailable = errors.New("currency: exchange rate unavailable")

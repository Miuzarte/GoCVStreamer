package main

func panicIf(err error) {
	if err != nil {
		log.Panic().
			Err(err).
			Msg("panicIf")
	}
}

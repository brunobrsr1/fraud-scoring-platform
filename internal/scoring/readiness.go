package scoring

func (s *Service) Ready() bool {
	// Check if the model is loaded and ready to score
	return s.model != nil && s.activeModel() != nil
}

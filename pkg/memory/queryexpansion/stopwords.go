package queryexpansion

var stopWords = buildStopWords()

func buildStopWords() map[string]struct{} {
	groups := [][]string{
		// English
		{"a", "an", "and", "are", "as", "at", "be", "by", "for", "from", "how", "in", "is", "it", "of", "on", "or", "that", "the", "this", "to", "was", "were", "what", "when", "where", "which", "who", "will", "with"},
		// Spanish
		{"de", "la", "que", "el", "en", "y", "los", "del", "se", "las", "por", "un", "para", "con", "no", "una", "su", "al", "lo"},
		// Portuguese
		{"o", "a", "os", "as", "um", "uma", "de", "da", "do", "dos", "das", "em", "com", "por", "para", "não", "nao", "que", "se", "ao"},
		// Arabic
		{"في", "من", "على", "إلى", "الى", "عن", "هذا", "هذه", "ذلك", "تلك", "هو", "هي", "ما", "ماذا", "كيف", "هل", "ثم", "و"},
		// Japanese
		{"これ", "それ", "あれ", "ここ", "そこ", "そして", "また", "です", "ます", "する"},
		// Korean
		{"이", "그", "저", "그리고", "또", "입니다", "합니다", "에서", "으로", "를", "을"},
		// Chinese
		{"这", "那", "和", "是", "在", "了", "吗", "什么", "怎么", "如何", "我们", "你们"},
	}
	out := map[string]struct{}{}
	for _, group := range groups {
		for _, token := range group {
			out[token] = struct{}{}
		}
	}
	return out
}

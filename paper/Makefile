TARG=gostor.pdf
.PRECIOUS: %.pdf

all: $(TARG)

%.pdf: %.tex %.bib
	pdflatex $<
	bibtex $*
	pdflatex $<
	bibtex $*
	pdflatex $<
	bibtex $*
	pdflatex $<

clean:
	rm -f *~ *.dvi *.log *.aux *.bbl *.blg

nuke: clean
	rm $(TARG)

distclean:
	rm -f *~ *.dvi *.log *.aux *.bbl *.blg $(TARG)

